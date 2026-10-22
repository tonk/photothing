// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"photothing/internal/gallery"
)

// sourceKeys collects every English text that can reach tr(): template {{t "..."}} calls, T(r, "...")
// calls, error messages passed to fail/denied, plus the keys that are chosen dynamically.
func sourceKeys(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{}
	add := func(re *regexp.Regexp, text, where string) {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			k := strings.ReplaceAll(m[1], `\"`, `"`)
			keys[k] = where
		}
	}
	tmpl := regexp.MustCompile(`\bt "((?:[^"\\]|\\.)*)"`)
	files, _ := filepath.Glob("templates/*.html")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		add(tmpl, string(b), f)
	}
	res := []*regexp.Regexp{
		regexp.MustCompile(`\bT\(r, "((?:[^"\\]|\\.)*)"`),
		regexp.MustCompile(`\.fail\(w, r, [^,]+, "((?:[^"\\]|\\.)*)"`),
		regexp.MustCompile(`denied\([^,]+, "((?:[^"\\]|\\.)*)"`),
		regexp.MustCompile(`v, msg, act :?= "[01]", "((?:[^"\\]|\\.)*)"`),
	}
	gofiles, _ := filepath.Glob("*.go")
	for _, f := range gofiles {
		if strings.HasSuffix(f, "_test.go") || f == "i18n_nl.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, re := range res {
			add(re, string(b), f)
		}
	}
	// Chosen at run time: roles, user states, theme names, HTTP status titles, photo information labels.
	for _, k := range []string{"viewer", "editor", "admin", "active", "disabled", "on", "off", "you", "system", "light", "dark",
		"Bad Request", "Unauthorized", "Forbidden", "Not Found", "Too Many Requests", "Internal Server Error",
		"Gallery now requires sign-in.", "Gallery is now publicly viewable (originals still require sign-in)."} {
		keys[k] = "dynamic"
	}
	// Photo information: the labels and the texts of the coded values come from the gallery package.
	for _, k := range gallery.ExifOrder {
		keys[k] = "gallery.ExifOrder"
	}
	for _, texts := range gallery.ExifValueTexts() {
		for _, k := range texts {
			keys[k] = "gallery.ExifValueTexts"
		}
	}
	return keys
}

func verbs(s string) string {
	return strings.Join(regexp.MustCompile(`%[a-z]`).FindAllString(s, -1), "")
}

func TestEveryTextHasADutchTranslation(t *testing.T) {
	keys := sourceKeys(t)
	if len(keys) < 100 {
		t.Fatalf("only %d texts found; the extraction is probably broken", len(keys))
	}
	for k, where := range keys {
		v, ok := nl[k]
		switch {
		case !ok:
			t.Errorf("missing Dutch translation for %q (%s)", k, where)
		case strings.TrimSpace(v) == "":
			t.Errorf("empty Dutch translation for %q", k)
		case verbs(k) != verbs(v):
			t.Errorf("format verbs differ: %q -> %q", k, v)
		}
	}
	for k := range nl {
		if _, used := keys[k]; !used {
			t.Errorf("translation for %q is not used by any template or handler", k)
		}
	}
}

func TestTranslate(t *testing.T) {
	if got := tr("nl", "Photo %d of %d", 2, 5); got != "Foto 2 van 5" {
		t.Errorf("got %q", got)
	}
	if got := tr("en", "Photo %d of %d", 2, 5); got != "Photo 2 of 5" {
		t.Errorf("got %q", got)
	}
	if got := tr("nl", "no such text"); got != "no such text" {
		t.Errorf("untranslated text must fall back to English, got %q", got)
	}
}
