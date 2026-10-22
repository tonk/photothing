// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

// Package web contains the HTTP handlers, templates and static assets.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/netip"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"photothing/internal/auth"
	"photothing/internal/gallery"
	"photothing/internal/store"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	cookieName    = "pt_session"
	langCookie    = "pt_lang"
	sessionTTL    = 14 * 24 * time.Hour
	mfaPendingTTL = 5 * time.Minute
	resetTTL      = time.Hour
	settingPublic = "public"
	appName       = "photothing"
	maxFormBytes  = 1 << 14
)

var pageNames = []string{"album", "photo", "search", "login", "mfa", "reset", "account", "admin", "audit", "access", "error"}

type Server struct {
	store   *store.Store
	lib     *gallery.Library
	box     *auth.Box
	about   aboutView
	secure  bool                                     // always set the Secure flag on cookies (use behind TLS)
	trusted []netip.Prefix                           // proxies whose X-Forwarded-* headers are believed
	pages   map[string]map[string]*template.Template // language -> page -> templates

	// Login throttles: per client IP (IPv6: per /64), per IP+username pair and (loosely, 200) per username. The pair key
	// keeps an attacker from locking the real user out of their own sign-in.
	ipThr   *auth.Throttle
	pairThr *auth.Throttle
	userThr *auth.Throttle
	// mfaThr counts failed second-factor attempts per user. A six-digit code is guessable, so this is much
	// stricter than the others: someone who knows the password must not be able to try codes from many addresses.
	mfaThr *auth.Throttle
	// logOnce lets the audit log mention a blocked visitor once per window instead of once per request.
	logOnce *auth.Throttle
}

// assetVersion hashes the embedded static files; it is part of their URLs so that browsers fetch
// new versions after an update and may otherwise cache them for a long time.
func assetVersion() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// About is what the About box shows about this build.
type About struct {
	Version    string // semantic version, without a leading "v"
	Commit     string
	RepoURL    string // where the source lives; shown as a link when it is an http(s) URL
	Author     string // shown in the About box when not empty
	License    string // SPDX identifier of the license, shown when not empty
	LicenseURL string // where the license text can be read; the license is linked when this is set
}

// aboutView is About prepared for the templates.
type aboutView struct {
	About
	RepoLink bool   // RepoURL can be opened in a browser
	Go       string // toolchain and platform
}

// Options configure the server.
type Options struct {
	About          About
	SecureCookies  bool           // mark cookies Secure even when HTTPS is not detected
	TrustedProxies []netip.Prefix // see ParseTrustedProxies
}

func New(st *store.Store, lib *gallery.Library, box *auth.Box, opts Options) (*Server, error) {
	ver := assetVersion()
	if opts.About.Version == "" {
		opts.About.Version = "0.0.0-dev"
	}
	if len(opts.About.Commit) > 12 && !strings.HasSuffix(opts.About.Commit, "-dirty") {
		opts.About.Commit = opts.About.Commit[:12]
	}
	s := &Server{store: st, lib: lib, box: box, secure: opts.SecureCookies, trusted: opts.TrustedProxies,
		about: aboutView{About: opts.About, Go: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
			RepoLink: strings.HasPrefix(opts.About.RepoURL, "https://") || strings.HasPrefix(opts.About.RepoURL, "http://")},
		pages:   map[string]map[string]*template.Template{},
		ipThr:   auth.NewThrottle(30, 15*time.Minute),
		pairThr: auth.NewThrottle(5, 15*time.Minute),
		userThr: auth.NewThrottle(200, 15*time.Minute),
		mfaThr:  auth.NewThrottle(10, 30*time.Minute),
		logOnce: auth.NewThrottle(1, 15*time.Minute)}
	for _, lang := range langs {
		lang := lang
		funcs := template.FuncMap{
			"t":         func(key string, args ...any) string { return tr(lang, key, args...) },
			"humanSize": humanSize,
			"osm":       osmURL,
			"stamp": func(t time.Time) string {
				if t.Unix() <= 0 {
					return "–" // e.g. sessions that were created before the creation time was recorded
				}
				return t.Local().Format("2006-01-02 15:04")
			},
			"v": func() string { return ver },
			// imgv is the ?v= part of picture URLs: the file's modification time and the rendering version, so a
			// browser fetches a picture again when the file or the way it is rendered has changed.
			"imgv": func(mtime int64) string {
				return strconv.FormatInt(mtime, 10) + "." + strconv.Itoa(gallery.RenderVersion)
			},
			"upper":    strings.ToUpper,
			"langName": langName,
		}
		s.pages[lang] = map[string]*template.Template{}
		for _, name := range pageNames {
			t, err := template.New("base.html").Funcs(funcs).ParseFS(tmplFS, "templates/base.html", "templates/"+name+".html")
			if err != nil {
				return nil, err
			}
			s.pages[lang][name] = t
		}
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticCache(http.FileServerFS(sub))))

	view, edit := auth.PermView, auth.PermEdit
	mux.HandleFunc("GET /{$}", s.require(view, s.handleAlbum))
	mux.HandleFunc("GET /a/{path...}", s.require(view, s.handleAlbum))
	mux.HandleFunc("GET /p/{id}", s.require(view, s.handlePhoto))
	mux.HandleFunc("POST /p/{id}/cover", s.require(edit, s.handleCover))
	mux.HandleFunc("POST /p/{id}/meta", s.require(edit, s.handleMeta))
	mux.HandleFunc("GET /img/{id}/{kind}", s.require(view, s.handleImage))
	mux.HandleFunc("GET /search", s.require(view, s.handleSearch))

	mux.HandleFunc("POST /prefs", s.handlePrefs)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /login/mfa", s.handleMFAForm)
	mux.HandleFunc("POST /login/mfa", s.handleMFA)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /reset", s.handleResetForm)
	mux.HandleFunc("POST /reset", s.handleReset)

	mux.HandleFunc("GET /account", s.requireLogin(s.handleAccount))
	mux.HandleFunc("POST /account/password", s.requireLogin(s.handlePassword))
	mux.HandleFunc("POST /account/mfa/start", s.requireLogin(s.handleMFAStart))
	mux.HandleFunc("POST /account/mfa/confirm", s.requireLogin(s.handleMFAConfirm))
	mux.HandleFunc("POST /account/mfa/disable", s.requireLogin(s.handleMFADisable))
	mux.HandleFunc("POST /account/mfa/recovery", s.requireLogin(s.handleRecoveryCodes))
	mux.HandleFunc("POST /account/sessions/revoke-others", s.requireLogin(s.handleRevokeOthers))
	mux.HandleFunc("POST /account/sessions/{handle}/revoke", s.requireLogin(s.handleRevokeSession))

	mux.HandleFunc("GET /admin", s.require(auth.PermScan, s.handleAdmin))
	mux.HandleFunc("POST /admin/scan", s.require(auth.PermScan, s.handleScan))
	users := auth.PermUsers
	mux.HandleFunc("POST /admin/public", s.require(users, s.handlePublic))
	mux.HandleFunc("POST /admin/users", s.require(users, s.handleUserCreate))
	mux.HandleFunc("POST /admin/users/{id}/role", s.require(users, s.handleUserRole))
	mux.HandleFunc("POST /admin/users/{id}/disable", s.require(users, s.handleUserDisable))
	mux.HandleFunc("POST /admin/users/{id}/mfa-reset", s.require(users, s.handleUserMFAReset))
	mux.HandleFunc("POST /admin/users/{id}/password", s.require(users, s.handleUserPassword))
	mux.HandleFunc("POST /admin/users/{id}/reset-link", s.require(users, s.handleUserResetLink))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.require(users, s.handleUserDelete))
	mux.HandleFunc("GET /admin/audit", s.require(users, s.handleAudit))
	mux.HandleFunc("GET /admin/access", s.require(users, s.handleAccess))
	mux.HandleFunc("POST /admin/access", s.require(users, s.handleAccessSave))
	mux.HandleFunc("POST /admin/access/delete", s.require(users, s.handleAccessDelete))

	// Reject cross-origin state-changing requests (CSRF) based on Fetch metadata / Origin.
	cop := http.NewCrossOriginProtection()
	return s.trustProxy(logRequests(securityHeaders(s.withState(cop.Handler(mux)))))
}

func (s *Server) publicMode() bool { return s.store.Setting(settingPublic) == "1" }

// ---- middleware ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// staticCache lets versioned URLs (?v=hash) be cached for a year; unversioned ones (the favicon) briefly.
func staticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter } // keeps Range/Flusher working

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") && !strings.HasPrefix(r.URL.Path, "/img/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

// reqState is what we know about the request's session; it is resolved once per request.
type reqState struct {
	Session *store.Session // nil when not signed in
	User    *store.User
	Pending bool // password accepted, MFA still to do
	Lang    string
}

type ctxKey struct{}

func (s *Server) withState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{}
		if c, err := r.Cookie(cookieName); err == nil {
			if se, err := s.store.SessionByHash(auth.HashToken(c.Value)); err == nil {
				if u, err := s.store.UserByID(se.UserID); err == nil && !u.Disabled {
					st.Session, st.User, st.Pending = se, u, se.MFAPending
				}
			}
		}
		st.Lang = s.pickLang(r, st.User)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, st)))
	})
}

func (s *Server) state(r *http.Request) *reqState {
	if st, ok := r.Context().Value(ctxKey{}).(*reqState); ok {
		return st
	}
	return &reqState{Lang: "en"}
}

// current returns the fully authenticated user for the request, or nil.
func (s *Server) current(r *http.Request) *store.User {
	if st := s.state(r); !st.Pending {
		return st.User
	}
	return nil
}

// require wraps a handler with an RBAC check. Anonymous visitors only get PermView, and only in public mode.
func (s *Server) require(perm auth.Perm, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.current(r)
		if user == nil {
			if perm == auth.PermView && s.publicMode() {
				h(w, r)
				return
			}
			s.redirectToLogin(w, r)
			return
		}
		if !auth.Can(user.Role, perm) {
			s.fail(w, r, http.StatusForbidden, "You do not have permission to do that.")
			return
		}
		h(w, r)
	}
}

func (s *Server) requireLogin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.current(r) == nil {
			s.redirectToLogin(w, r)
			return
		}
		h(w, r)
	}
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	target := "/login"
	if r.Method == http.MethodGet {
		target += "?next=" + urlEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ---- rendering ----

type page struct {
	Title  string
	User   *store.User
	Public bool
	Flash  string
	Error  string
	Data   any
	Lang   string
	Langs  []string
	About  aboutView
	Path   string // current request URI, for the language switch
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.User = s.current(r)
	p.Public = s.publicMode()
	p.Lang, p.Langs, p.Path, p.About = s.state(r).Lang, langs, r.URL.RequestURI(), s.about
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.pages[p.Lang][name].ExecuteTemplate(w, "base", p); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.WriteHeader(code)
	s.render(w, r, "error", page{Title: s.T(r, http.StatusText(code)), Error: s.T(r, msg)})
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	s.fail(w, r, http.StatusInternalServerError, "Something went wrong.")
}

// ---- helpers ----

type crumb struct{ Name, Path string }

func crumbs(dir string) []crumb {
	var out []crumb
	if dir == "" {
		return out
	}
	acc := ""
	for _, part := range strings.Split(dir, "/") {
		acc = path.Join(acc, part)
		out = append(out, crumb{Name: part, Path: acc})
	}
	return out
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return ftoa(float64(n)/float64(div)) + " " + string("KMGT"[exp]) + "B"
}
