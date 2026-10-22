// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

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

// auditBlocked records that sign-ins from this source are being refused, once per window and source:
// a blocked client can keep sending requests, and each one must not become a row.
func (s *Server) auditBlocked(r *http.Request, action, target, source string) {
	key := action + "|" + source
	if s.logOnce.Blocked(key) {
		return
	}
	s.logOnce.Fail(key)
	s.auditAs(r, "-", action, target, "")
}
