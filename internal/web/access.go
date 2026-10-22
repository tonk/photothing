package web

import (
	"path"

	"photothing/internal/store"
)

// access evaluates the per-album restrictions. An album with a rule is visible to admins and the
// listed users only; the rule applies to everything below it, and every restricted ancestor must agree.
type access struct{ rules map[string]map[int64]bool }

func (s *Server) loadAccess() (*access, error) {
	rules, err := s.store.AccessRules()
	return &access{rules: rules}, err
}

func (a *access) allowed(u *store.User, dir string) bool {
	if len(a.rules) == 0 {
		return true
	}
	for d := dir; d != ""; d = parentDir(d) {
		users, restricted := a.rules[d]
		if !restricted {
			continue
		}
		if u == nil || (u.Role != "admin" && !users[u.ID]) {
			return false
		}
	}
	return true
}

func (a *access) filter(u *store.User) func(string) bool {
	return func(dir string) bool { return a.allowed(u, dir) }
}

func parentDir(d string) string {
	p := path.Dir(d)
	if p == "." || p == "/" {
		return ""
	}
	return p
}
