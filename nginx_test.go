// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// nginxDirectives returns the lines of an nginx file without comments and blank lines.
func nginxDirectives(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// nginx cannot be run here, so the examples are held to the rules whose breaking has bitten before: with several
// instances the rate-limit zone must be defined exactly once, outside any server block, with a size, and the
// instance file (which is copied per instance) may only refer to it. Otherwise nginx stops with
// "zero size shared memory zone".
func TestNginxExamplesAgreeOnTheRateLimitZone(t *testing.T) {
	limits := nginxDirectives(t, "contrib/nginx-limits.conf.example")
	instance := nginxDirectives(t, "contrib/nginx.conf.example")

	zoneDef := regexp.MustCompile(`^limit_req_zone\s+\S+\s+zone=([A-Za-z0-9_]+):(\d+)([kKmM]?)\s+rate=\d+r/[sm];$`)
	defined := map[string]bool{}
	depth := 0
	for _, l := range limits {
		if strings.HasPrefix(l, "limit_req_zone") {
			m := zoneDef.FindStringSubmatch(l)
			if m == nil {
				t.Fatalf("not a valid limit_req_zone directive: %q", l)
			}
			if m[2] == "0" {
				t.Errorf("zone %s has size zero", m[1])
			}
			if depth != 0 {
				t.Errorf("limit_req_zone must be in the http context, not inside a block: %q", l)
			}
			if defined[m[1]] {
				t.Errorf("zone %s is defined twice", m[1])
			}
			defined[m[1]] = true
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
	}
	if len(defined) != 1 {
		t.Errorf("%d zones defined in nginx-limits.conf.example, want 1: %v", len(defined), defined)
	}

	use := regexp.MustCompile(`^limit_req\s+zone=([A-Za-z0-9_]+)\b`)
	used := 0
	for _, l := range instance {
		if strings.HasPrefix(l, "limit_req_zone") {
			t.Errorf("the instance file must not define the zone (it is copied per instance): %q", l)
		}
		if m := use.FindStringSubmatch(l); m != nil {
			used++
			if !defined[m[1]] {
				t.Errorf("the instance file uses zone %q, which nginx-limits.conf.example does not define", m[1])
			}
		}
	}
	if used == 0 {
		t.Error("the instance file no longer uses the rate-limit zone")
	}
}

// A minimal syntax check of both examples: braces balance and every line ends the way an nginx line does.
func TestNginxExamplesLookLikeNginx(t *testing.T) {
	for _, path := range []string{"contrib/nginx.conf.example", "contrib/nginx-limits.conf.example"} {
		depth := 0
		for _, l := range nginxDirectives(t, path) {
			if !strings.HasSuffix(l, ";") && !strings.HasSuffix(l, "{") && l != "}" {
				t.Errorf("%s: %q does not end in ';', '{' or '}'", path, l)
			}
			depth += strings.Count(l, "{") - strings.Count(l, "}")
			if depth < 0 {
				t.Errorf("%s: a '}' without its '{' at %q", path, l)
			}
		}
		if depth != 0 {
			t.Errorf("%s: braces do not balance (%d open)", path, depth)
		}
	}
	// the proxy_pass of the instance file points at an upstream that the file defines
	instance := strings.Join(nginxDirectives(t, "contrib/nginx.conf.example"), "\n")
	for _, m := range regexp.MustCompile(`proxy_pass http://(\w+);`).FindAllStringSubmatch(instance, -1) {
		if !strings.Contains(instance, "upstream "+m[1]+" {") {
			t.Errorf("proxy_pass points at upstream %q, which is not defined", m[1])
		}
	}
}

// Both examples must reach the users: in the packages and in the documentation.
func TestNginxExamplesAreShipped(t *testing.T) {
	nfpm, _ := os.ReadFile("packaging/nfpm.yaml")
	readme, _ := os.ReadFile("README.md")
	for _, f := range []string{"nginx.conf.example", "nginx-limits.conf.example"} {
		if !strings.Contains(string(nfpm), "/usr/share/doc/photothing/"+f) {
			t.Errorf("packaging/nfpm.yaml does not install %s", f)
		}
		if !strings.Contains(string(readme), f) {
			t.Errorf("README.md does not mention %s", f)
		}
	}
}
