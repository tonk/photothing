// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Behind a reverse proxy every connection comes from the proxy, so the real visitor is only known from
// headers the proxy adds. Those headers are only believed when the connection itself comes from a
// proxy the administrator listed (-trusted-proxy); from anyone else they are ignored, because a
// visitor could otherwise make up an address and escape the login limits.

// ParseTrustedProxies reads a comma-separated list of addresses and CIDR ranges ("127.0.0.1,10.0.0.0/8,::1").
func ParseTrustedProxies(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not an IP address or CIDR range", item)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseAddr accepts "1.2.3.4", "1.2.3.4:80", "[::1]:80" and "::1".
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.WithZone("").Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().WithZone("").Unmap(), true
	}
	return netip.Addr{}, false
}

// realIP determines the visitor behind a trusted proxy. xff holds all X-Forwarded-For header lines.
// The addresses are read from the right, skipping proxies we trust; the first other address is the
// visitor. Everything left of it was supplied by the visitor and is not believed. If a header is not
// valid, the proxy's own address is used.
func realIP(peer netip.Addr, xff []string, xRealIP string, trusted []netip.Prefix) netip.Addr {
	var hops []string
	for _, line := range xff {
		for _, h := range strings.Split(line, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	if len(hops) == 0 {
		if a, ok := parseAddr(xRealIP); ok {
			return a
		}
		return peer
	}
	var last netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(hops[i])
		if !ok {
			return peer
		}
		if !isTrusted(a, trusted) {
			return a
		}
		last = a
	}
	return last // every hop was a trusted proxy: the leftmost one is the best we know
}

type httpsKey struct{}

// trustProxy rewrites r.RemoteAddr to the visitor's address (and notes whether the visitor used HTTPS)
// for requests that come from a trusted proxy. Other requests pass through untouched.
func (s *Server) trustProxy(next http.Handler) http.Handler {
	if len(s.trusted) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		peer, ok := parseAddr(host)
		if err != nil || !ok || !isTrusted(peer, s.trusted) {
			next.ServeHTTP(w, r)
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		r2.RemoteAddr = net.JoinHostPort(realIP(peer, r.Header.Values("X-Forwarded-For"), r.Header.Get("X-Real-IP"), s.trusted).String(), "0")
		proto := r.Header.Values("X-Forwarded-Proto")
		https := len(proto) > 0 && strings.EqualFold(strings.TrimSpace(proto[len(proto)-1]), "https")
		next.ServeHTTP(w, r2.WithContext(context.WithValue(r.Context(), httpsKey{}, https)))
	})
}

// isHTTPS reports whether the visitor reached us over HTTPS, directly or through a trusted proxy.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	v, _ := r.Context().Value(httpsKey{}).(bool)
	return v
}

// secureCookies reports whether cookies must carry the Secure flag for this request.
func (s *Server) secureCookies(r *http.Request) bool { return s.secure || s.isHTTPS(r) }
