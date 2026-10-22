package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"photothing/internal/auth"
	"photothing/internal/store"
)

type adminData struct {
	Users     []store.User // only filled for users who may manage users
	Roles     []string
	CanManage bool
	PublicOn  bool
	Self      int64
	ResetURL  string // a freshly issued password reset link, shown once
	ResetFor  string
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, p page) {
	s.renderAdminWith(w, r, p, adminData{})
}

func (s *Server) renderAdminWith(w http.ResponseWriter, r *http.Request, p page, d adminData) {
	u := s.current(r)
	d.CanManage, d.PublicOn, d.Roles, d.Self = auth.Can(u.Role, auth.PermUsers), s.publicMode(), []string{"viewer", "editor", "admin"}, u.ID
	if d.CanManage {
		users, err := s.store.ListUsers()
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Users = users
	}
	p.Title, p.Data = s.T(r, "Admin"), d
	if p.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "admin", p)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.renderAdmin(w, r, page{})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	res, err := s.lib.Scan(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	go s.lib.PruneCache()
	s.audit(r, "scan", "", res.String())
	msg := s.T(r, "Scan finished: %d added, %d updated, %d moved, %d removed, %d skipped.", res.Added, res.Updated, res.Moved, res.Removed, res.Skipped)
	for _, tool := range []string{"heif", "raw", "video"} {
		if n := res.MissingTools[tool]; n > 0 {
			msg += " " + s.T(r, "%d file(s) were skipped because the %s tools are not installed.", n, tool)
		}
	}
	s.renderAdmin(w, r, page{Flash: msg})
}

func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	v, msg, act := "0", "Gallery now requires sign-in.", "public.off"
	if r.PostFormValue("public") == "1" {
		v, msg, act = "1", "Gallery is now publicly viewable (originals still require sign-in).", "public.on"
	}
	if err := s.store.SetSetting(settingPublic, v); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, act, "", "")
	s.renderAdmin(w, r, page{Flash: s.T(r, msg)})
}

// ---- user management (users:manage) ----

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	name, role, pw := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("role"), r.PostFormValue("password")
	switch {
	case name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsAny(name, " \t\r\n/\\|"):
		s.renderAdmin(w, r, page{Error: s.T(r, "Username must be 1–64 characters without spaces or slashes.")})
		return
	case !auth.ValidRole(role):
		s.renderAdmin(w, r, page{Error: s.T(r, "Unknown role.")})
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.renderAdmin(w, r, page{Error: s.T(r, "Password must be at least %d characters.", auth.MinPasswordLen)})
		return
	}
	if _, err := s.store.UserByName(name); err == nil {
		s.renderAdmin(w, r, page{Error: s.T(r, "That username already exists.")})
		return
	}
	if err := s.store.CreateUser(name, hash, role); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "user.create", name, role)
	s.renderAdmin(w, r, page{Flash: s.T(r, "Created %s (%s).", name, s.T(r, role))})
}

// targetUser loads the user from the path. Admins may not change their own account here,
// so they cannot lock themselves out; use the CLI or /account for that.
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) *store.User {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "User not found.")
		return nil
	}
	t, err := s.store.UserByID(id)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "User not found.")
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	if t.ID == s.current(r).ID {
		s.renderAdmin(w, r, page{Error: s.T(r, "You cannot do that to your own account.")})
		return nil
	}
	return t
}

func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	role := r.PostFormValue("role")
	if !auth.ValidRole(role) {
		s.renderAdmin(w, r, page{Error: s.T(r, "Unknown role.")})
		return
	}
	if err := s.store.SetRole(t.ID, role); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "user.role", t.Username, t.Role+" -> "+role)
	s.renderAdmin(w, r, page{Flash: s.T(r, "%s is now %s.", t.Username, s.T(r, role))})
}

func (s *Server) handleUserDisable(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	disable := r.PostFormValue("disabled") == "1"
	if err := s.store.SetDisabled(t.ID, disable); err != nil {
		s.serverError(w, r, err)
		return
	}
	if disable {
		s.audit(r, "user.disable", t.Username, "")
		s.renderAdmin(w, r, page{Flash: s.T(r, "%s disabled and signed out.", t.Username)})
		return
	}
	s.audit(r, "user.enable", t.Username, "")
	s.renderAdmin(w, r, page{Flash: s.T(r, "%s enabled.", t.Username)})
}

func (s *Server) handleUserMFAReset(w http.ResponseWriter, r *http.Request) {
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	if err := s.store.SetTOTP(t.ID, "", false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.DeleteUserSessions(t.ID)
	s.audit(r, "user.mfa-reset", t.Username, "")
	s.renderAdmin(w, r, page{Flash: s.T(r, "MFA reset for %s.", t.Username)})
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	hash, err := auth.HashPassword(r.PostFormValue("password"))
	if err != nil {
		s.renderAdmin(w, r, page{Error: s.T(r, "Password must be at least %d characters.", auth.MinPasswordLen)})
		return
	}
	if err := s.store.SetPassword(t.ID, hash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "user.password", t.Username, "")
	s.renderAdmin(w, r, page{Flash: s.T(r, "Password set for %s; their sessions were signed out.", t.Username)})
}

// handleUserResetLink creates a one-hour, single-use link the user can open to choose a new password.
// There is no e-mail: the admin passes the link on.
func (s *Server) handleUserResetLink(w http.ResponseWriter, r *http.Request) {
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	token, hash := auth.NewToken()
	if err := s.store.CreateReset(hash, t.ID, timeNow().Add(resetTTL)); err != nil {
		s.serverError(w, r, err)
		return
	}
	scheme := "http"
	if s.secureCookies(r) {
		scheme = "https"
	}
	link := scheme + "://" + r.Host + "/reset?t=" + token
	s.audit(r, "user.reset-link", t.Username, "")
	s.renderAdminWith(w, r, page{}, adminData{ResetURL: link, ResetFor: t.Username})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	t := s.targetUser(w, r)
	if t == nil {
		return
	}
	if err := s.store.DeleteUser(t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "user.delete", t.Username, "")
	s.renderAdmin(w, r, page{Flash: s.T(r, "Deleted %s.", t.Username)})
}

// ---- audit log ----

const auditPageSize = 50

type auditData struct {
	Entries []store.AuditEntry
	Pager   pager
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	pg := pageParam(r)
	entries, total, err := s.store.ListAudit(auditPageSize, (pg-1)*auditPageSize)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := newPager(total, auditPageSize, pg, "/admin/audit?")
	if p.Page != pg { // beyond the last page
		entries, _, err = s.store.ListAudit(auditPageSize, (p.Page-1)*auditPageSize)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.render(w, r, "audit", page{Title: s.T(r, "Audit log"), Data: auditData{Entries: entries, Pager: p}})
}

// ---- album access ----

type accessRule struct {
	Dir     string
	Allowed map[int64]bool
}

type accessData struct {
	Rules   []accessRule
	Dirs    []string     // albums that are not restricted yet
	Members []store.User // users a rule can name (admins always have access)
}

func (s *Server) renderAccess(w http.ResponseWriter, r *http.Request, p page) {
	rules, err := s.store.AccessRules()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stats, err := s.store.DirStats()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var d accessData
	for _, u := range users {
		if u.Role != "admin" {
			d.Members = append(d.Members, u)
		}
	}
	for dir, allowed := range rules {
		d.Rules = append(d.Rules, accessRule{Dir: dir, Allowed: allowed})
	}
	sort.Slice(d.Rules, func(i, j int) bool { return d.Rules[i].Dir < d.Rules[j].Dir })
	all := map[string]bool{}
	for _, st := range stats {
		for dir := st.Dir; dir != ""; dir = parentDir(dir) {
			all[dir] = true
		}
	}
	for dir := range all {
		if _, restricted := rules[dir]; !restricted {
			d.Dirs = append(d.Dirs, dir)
		}
	}
	sort.Strings(d.Dirs)
	p.Title, p.Data = s.T(r, "Album access"), d
	if p.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "access", p)
}

func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request) {
	s.renderAccess(w, r, page{})
}

func (s *Server) handleAccessSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	dir := strings.Trim(r.PostFormValue("dir"), "/")
	if dir == "" || strings.Contains(dir, "..") {
		s.renderAccess(w, r, page{Error: s.T(r, "Choose an album.")})
		return
	}
	var allowed []int64
	var names []string
	for _, v := range r.PostForm["user"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		u, err := s.store.UserByID(id)
		if err != nil || u.Role == "admin" {
			continue
		}
		allowed = append(allowed, id)
		names = append(names, u.Username)
	}
	if err := s.store.SetAccess(dir, allowed); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "access.set", dir, strings.Join(names, ","))
	s.renderAccess(w, r, page{Flash: s.T(r, "Access to %s saved.", dir)})
}

func (s *Server) handleAccessDelete(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	dir := strings.Trim(r.PostFormValue("dir"), "/")
	if err := s.store.DeleteAccess(dir); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "access.remove", dir, "")
	s.renderAccess(w, r, page{Flash: s.T(r, "%s is no longer restricted.", dir)})
}
