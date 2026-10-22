// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func itoa(n int) string         { return strconv.Itoa(n) }
func ftoa(f float64) string     { return strconv.FormatFloat(f, 'f', 1, 64) }
func urlEscape(s string) string { return url.QueryEscape(s) }

// safeNext only allows local, absolute paths as redirect targets; anything else becomes "/".
// Browsers delete tabs and line breaks from a URL before parsing it, so "/<tab>/evil.example" would turn
// into the protocol-relative "//evil.example": control characters are refused outright.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return next
}

// osmURL turns "52.1, 4.5" into an OpenStreetMap link; anything else yields "".
func osmURL(gps string) string {
	lat, lon, ok := strings.Cut(gps, ", ")
	if !ok {
		return ""
	}
	la, err1 := strconv.ParseFloat(lat, 64)
	lo, err2 := strconv.ParseFloat(lon, 64)
	if err1 != nil || err2 != nil {
		return ""
	}
	return "https://www.openstreetmap.org/?mlat=" + ftoa5(la) + "&mlon=" + ftoa5(lo) + "#map=15/" + ftoa5(la) + "/" + ftoa5(lo)
}

func ftoa5(f float64) string { return strconv.FormatFloat(f, 'f', 5, 64) }

func clientIP(r *http.Request) string {
	// X-Forwarded-For is deliberately not trusted: it is client-controlled unless a proxy sets it.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// throttleAddr is the address sign-in limits are counted on. An IPv6 network hands a single customer a
// whole /64 block (billions of addresses); counting every address on its own would let one attacker
// step around the limits, so IPv6 addresses are counted per /64.
func throttleAddr(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Is4() || a.Is4In6() {
		return ip
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// cleanText trims text and removes control characters; it returns "" when nothing is left.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func atoi(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

func timeNow() time.Time { return time.Now() }
