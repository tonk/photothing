package web

import (
	"log"
	"net/http"

	"photothing/internal/store"
)

// audit records a security-relevant event; the actor is the signed-in user (or "-").
func (s *Server) audit(r *http.Request, action, target, detail string) {
	actor := "-"
	if u := s.state(r).User; u != nil {
		actor = u.Username
	}
	s.auditAs(r, actor, action, target, detail)
}

func (s *Server) auditAs(r *http.Request, actor, action, target, detail string) {
	if err := s.store.AddAudit(store.AuditEntry{Actor: actor, Action: action, Target: target, Detail: detail, IP: clientIP(r)}); err != nil {
		log.Printf("audit %s: %v", action, err)
	}
}
