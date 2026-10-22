// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"

	"photothing/internal/auth"
	"photothing/internal/store"
)

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
}

// startSession always issues a fresh token (prevents session fixation) and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64, mfaPending bool) error {
	ttl := sessionTTL
	if mfaPending {
		ttl = mfaPendingTTL
	}
	token, hash := auth.NewToken()
	exp := time.Now().Add(ttl)
	if err := s.store.CreateSession(hash, userID, mfaPending, exp, clientIP(r), r.UserAgent()); err != nil {
		return err
	}
	s.setCookie(w, r, token, exp)
	// Make the rest of this request see the new session (e.g. to render the account page after a password change).
	st := s.state(r)
	st.Session, st.Pending = &store.Session{TokenHash: hash, UserID: userID, MFAPending: mfaPending}, mfaPending
	return nil
}

func (s *Server) endCurrentSession(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.store.DeleteSession(auth.HashToken(c.Value))
	}
}

// ---- throttling ----

func (s *Server) throttled(ip, name string) bool {
	return s.ipThr.Blocked(ip) || s.pairThr.Blocked(ip+"|"+name) || s.userThr.Blocked(name)
}

func (s *Server) failed(ip, name string) {
	s.ipThr.Fail(ip)
	s.pairThr.Fail(ip + "|" + name)
	s.userThr.Fail(name)
}

func (s *Server) succeeded(ip, name string) { s.pairThr.Reset(ip + "|" + name) }

// ---- sign in ----

type loginData struct{ Next string }

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.current(r) != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", page{Title: s.T(r, "Sign in"), Data: loginData{Next: safeNext(r.URL.Query().Get("next"))}})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "Bad request.")
		return
	}
	name, pw, next := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password"), safeNext(r.PostFormValue("next"))
	ip, nameKey := clientIP(r), strings.ToLower(name)

	denied := func(code int, msg string) {
		w.WriteHeader(code)
		s.render(w, r, "login", page{Title: s.T(r, "Sign in"), Error: s.T(r, msg), Data: loginData{Next: next}})
	}
	if s.throttled(ip, nameKey) {
		s.auditAs(r, "-", "login.blocked", name, "")
		denied(http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	u, err := s.store.UserByName(name)
	if err != nil || u.Disabled {
		auth.CheckDummy(pw)
		s.failed(ip, nameKey)
		s.auditAs(r, "-", "login.fail", name, "")
		denied(http.StatusUnauthorized, "Invalid username or password.")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, pw) {
		s.failed(ip, nameKey)
		s.auditAs(r, "-", "login.fail", name, "")
		denied(http.StatusUnauthorized, "Invalid username or password.")
		return
	}
	s.endCurrentSession(r)
	if u.TOTPEnabled {
		if err := s.startSession(w, r, u.ID, true); err != nil {
			s.serverError(w, r, err)
			return
		}
		http.Redirect(w, r, "/login/mfa?next="+urlEscape(next), http.StatusSeeOther)
		return
	}
	s.succeeded(ip, nameKey)
	if err := s.startSession(w, r, u.ID, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.auditAs(r, u.Username, "login.ok", u.Username, "")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleMFAForm(w http.ResponseWriter, r *http.Request) {
	if st := s.state(r); !st.Pending {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "mfa", page{Title: s.T(r, "Two-factor authentication"), Data: loginData{Next: safeNext(r.URL.Query().Get("next"))}})
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "Bad request.")
		return
	}
	st := s.state(r)
	if !st.Pending {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	u := st.User
	next := safeNext(r.PostFormValue("next"))
	norm := auth.NormalizeRecovery(r.PostFormValue("code"))
	ip, nameKey := clientIP(r), strings.ToLower(u.Username)

	denied := func(code int, msg string) {
		w.WriteHeader(code)
		s.render(w, r, "mfa", page{Title: s.T(r, "Two-factor authentication"), Error: s.T(r, msg), Data: loginData{Next: next}})
	}
	if s.throttled(ip, nameKey) {
		s.store.DeleteSession(st.Session.TokenHash)
		s.auditAs(r, u.Username, "mfa.blocked", u.Username, "")
		denied(http.StatusTooManyRequests, "Too many failed attempts. Sign in again later.")
		return
	}

	ok, usedRecovery := false, false
	switch {
	case auth.LooksLikeRecovery(norm):
		fresh, err := s.store.UseRecoveryCode(u.ID, auth.HashRecovery(norm))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		ok, usedRecovery = fresh, fresh
	case len(norm) == 6:
		secret, err := s.box.Open(u.TOTPSecret)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if step, match := auth.MatchTOTP(secret, norm, time.Now()); match {
			fresh, err := s.store.ConsumeTOTPStep(u.ID, step)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			ok = fresh // a replayed code is rejected
		}
	}
	if !ok {
		s.failed(ip, nameKey)
		s.auditAs(r, u.Username, "mfa.fail", u.Username, "")
		denied(http.StatusUnauthorized, "Invalid code.")
		return
	}
	s.store.DeleteSession(st.Session.TokenHash)
	s.succeeded(ip, nameKey)
	if err := s.startSession(w, r, u.ID, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.auditAs(r, u.Username, "login.ok", u.Username, "mfa")
	if usedRecovery {
		s.auditAs(r, u.Username, "mfa.recovery-used", u.Username, "")
		next = "/account" // shows how many recovery codes are left
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u := s.current(r); u != nil {
		s.audit(r, "logout", u.Username, "")
	}
	s.endCurrentSession(r)
	s.clearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- preferences (language, page size, sort order) ----

func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	next := safeNext(r.PostFormValue("next"))
	u := s.current(r)

	lang := r.PostFormValue("lang")
	if validLang(lang) {
		s.setLangCookie(w, r, lang)
	} else {
		lang = ""
	}
	if u != nil {
		newLang, size, mode := u.Language, u.PageSize, u.Sort
		if lang != "" {
			newLang = lang
		}
		if n, err := atoi(r.PostFormValue("page_size")); err == nil && validPageSize(n) {
			size = n
		}
		if m := r.PostFormValue("sort"); validSort(m) {
			mode = m
		}
		if err := s.store.SetPrefs(u.ID, newLang, size, mode); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// ---- password reset links (issued by an admin) ----

type resetData struct{ Token string }

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if _, err := s.store.ResetUser(auth.HashToken(token)); err != nil {
		s.ipThr.Fail(clientIP(r))
		s.fail(w, r, http.StatusNotFound, "This reset link is invalid or has expired.")
		return
	}
	s.render(w, r, "reset", page{Title: s.T(r, "Choose a new password"), Data: resetData{Token: token}})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	token, pw, pw2 := r.PostFormValue("t"), r.PostFormValue("new"), r.PostFormValue("confirm")
	ip := clientIP(r)
	if s.ipThr.Blocked(ip) {
		s.fail(w, r, http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	u, err := s.store.ResetUser(auth.HashToken(token))
	if err != nil {
		s.ipThr.Fail(ip)
		s.fail(w, r, http.StatusNotFound, "This reset link is invalid or has expired.")
		return
	}
	again := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "reset", page{Title: s.T(r, "Choose a new password"), Error: msg, Data: resetData{Token: token}})
	}
	if pw != pw2 {
		again(s.T(r, "New passwords do not match."))
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		again(s.T(r, "Password must be at least %d characters.", auth.MinPasswordLen))
		return
	}
	if err := s.store.SetPassword(u.ID, hash); err != nil { // also deletes the link and all sessions
		s.serverError(w, r, err)
		return
	}
	s.auditAs(r, u.Username, "password.reset", u.Username, "link")
	s.render(w, r, "login", page{Title: s.T(r, "Sign in"), Flash: s.T(r, "Password changed. You can sign in now."), Data: loginData{Next: "/"}})
}

// ---- account ----

type sessionView struct {
	Handle    string
	Created   time.Time
	IP        string
	UserAgent string
	Current   bool
}

type accountData struct {
	User        *store.User
	QR          template.URL // data: URI of the enrolment QR code
	Secret      string
	Codes       []string // fresh recovery codes, shown once
	CodesLeft   int
	Sessions    []sessionView
	Sizes       []int
	SortModes   []string
	PageSize    int
	SortMode    string
	Languages   []string
	CurrentLang string
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, code int, p page, d accountData) {
	u := s.current(r)
	d.User = u
	d.Sizes, d.SortModes, d.Languages = pageSizes, sortModes, langs
	d.PageSize, d.SortMode = prefs(u)
	d.CurrentLang = s.state(r).Lang
	d.CodesLeft = s.store.CountRecoveryCodes(u.ID)
	cur := s.state(r).Session.TokenHash
	sessions, err := s.store.ListSessions(u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, se := range sessions {
		d.Sessions = append(d.Sessions, sessionView{Handle: se.TokenHash[:16], Created: se.CreatedAt, IP: se.IP,
			UserAgent: shortAgent(se.UserAgent), Current: se.TokenHash == cur})
	}
	p.Title, p.Data = s.T(r, "Account"), d
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	s.render(w, r, "account", p)
}

// shortAgent reduces a User-Agent header to a recognisable browser and OS.
func shortAgent(ua string) string {
	if ua == "" {
		return ""
	}
	browser := "Browser"
	for _, b := range []struct{ token, name string }{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"}, {"curl/", "curl"}} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, o := range []struct{ token, name string }{{"Windows", "Windows"}, {"Android", "Android"}, {"iPhone", "iOS"}, {"iPad", "iOS"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, o.token) {
			system = o.name
			break
		}
	}
	if system == "" {
		return browser
	}
	return browser + " / " + system
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, page{}, accountData{})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	u := s.current(r)
	cur, pw, pw2 := r.PostFormValue("current"), r.PostFormValue("new"), r.PostFormValue("confirm")
	if !auth.CheckPassword(u.PasswordHash, cur) {
		s.renderAccount(w, r, http.StatusUnauthorized, page{Error: s.T(r, "Current password is incorrect.")}, accountData{})
		return
	}
	if pw != pw2 {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "New passwords do not match.")}, accountData{})
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "Password must be at least %d characters.", auth.MinPasswordLen)}, accountData{})
		return
	}
	if err := s.store.SetPassword(u.ID, hash); err != nil { // revokes every session, including this one
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, u.ID, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "password.change", u.Username, "")
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "Password changed. Other sessions were signed out.")}, accountData{})
}

func (s *Server) handleMFAStart(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	if u.TOTPEnabled {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "Two-factor authentication is already enabled.")}, accountData{})
		return
	}
	key, err := auth.NewTOTP(appName, u.Username)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetTOTP(u.ID, s.box.Seal(key.Secret()), false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAccount(w, r, http.StatusOK, page{}, accountData{QR: qrDataURI(key), Secret: key.Secret()})
}

// issueRecoveryCodes replaces the user's recovery codes and returns the new ones in plain text.
func (s *Server) issueRecoveryCodes(u *store.User) ([]string, error) {
	codes, hashes := auth.NewRecoveryCodes(10)
	return codes, s.store.ReplaceRecoveryCodes(u.ID, hashes)
}

func (s *Server) handleMFAConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	u := s.current(r)
	if u.TOTPEnabled || u.TOTPSecret == "" {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "Start the setup first.")}, accountData{})
		return
	}
	secret, err := s.box.Open(u.TOTPSecret)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	code := strings.ReplaceAll(strings.TrimSpace(r.PostFormValue("code")), " ", "")
	step, ok := auth.MatchTOTP(secret, code, time.Now())
	if !ok {
		key, _ := otp.NewKeyFromURL("otpauth://totp/" + appName + ":" + urlEscape(u.Username) + "?secret=" + secret + "&issuer=" + appName)
		s.renderAccount(w, r, http.StatusUnauthorized, page{Error: s.T(r, "Invalid code, try again.")}, accountData{QR: qrDataURI(key), Secret: secret})
		return
	}
	if err := s.store.SetTOTP(u.ID, u.TOTPSecret, true); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.ConsumeTOTPStep(u.ID, step)
	u.TOTPEnabled = true
	codes, err := s.issueRecoveryCodes(u)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "mfa.enable", u.Username, "")
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "Two-factor authentication is now enabled.")}, accountData{Codes: codes})
}

func (s *Server) handleRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	u := s.current(r)
	if !u.TOTPEnabled {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "Two-factor authentication is not enabled.")}, accountData{})
		return
	}
	if !auth.CheckPassword(u.PasswordHash, r.PostFormValue("current")) {
		s.renderAccount(w, r, http.StatusUnauthorized, page{Error: s.T(r, "Password is incorrect.")}, accountData{})
		return
	}
	codes, err := s.issueRecoveryCodes(u)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "mfa.recovery-regenerate", u.Username, "")
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "New recovery codes generated. The old ones no longer work.")}, accountData{Codes: codes})
}

func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	u := s.current(r)
	if !auth.CheckPassword(u.PasswordHash, r.PostFormValue("current")) {
		s.renderAccount(w, r, http.StatusUnauthorized, page{Error: s.T(r, "Password is incorrect.")}, accountData{})
		return
	}
	if err := s.store.SetTOTP(u.ID, "", false); err != nil {
		s.serverError(w, r, err)
		return
	}
	u.TOTPEnabled = false
	s.audit(r, "mfa.disable", u.Username, "")
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "Two-factor authentication disabled.")}, accountData{})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	handle := r.PathValue("handle")
	if strings.HasPrefix(s.state(r).Session.TokenHash, handle) {
		s.renderAccount(w, r, http.StatusBadRequest, page{Error: s.T(r, "Use Sign out to end this session.")}, accountData{})
		return
	}
	ok, err := s.store.DeleteSessionByPrefix(u.ID, handle)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if ok {
		s.audit(r, "session.revoke", u.Username, "")
	}
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "Session signed out.")}, accountData{})
}

func (s *Server) handleRevokeOthers(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	if err := s.store.DeleteOtherSessions(u.ID, s.state(r).Session.TokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "session.revoke-others", u.Username, "")
	s.renderAccount(w, r, http.StatusOK, page{Flash: s.T(r, "All other sessions were signed out.")}, accountData{})
}

func qrDataURI(key *otp.Key) template.URL {
	if key == nil {
		return ""
	}
	img, err := key.Image(220, 220)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()))
}
