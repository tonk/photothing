// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The header reads "Logo  <title> - PhotoThing" with a site title and "Logo  PhotoThing" without; the browser tab
// shows "<page> · <title>" or "<page> · PhotoThing". The footer and the About box are about the software and
// keep saying PhotoThing.
func TestSiteTitleInTheHeaderAndTheTab(t *testing.T) {
	e := newEnvWith(t, Options{SiteTitle: "Familiefoto's"})
	vera := e.login("vera")
	for path, tab := range map[string]string{"/": "Albums", "/account": "Account", "/search": "Search"} {
		body := vera.mustGet(path)
		for _, want := range []string{
			`<a class="brand titled" href="/">`,
			`<span class="site">Familiefoto&#39;s</span><span class="sep" aria-hidden="true">–</span><span class="product">PhotoThing</span>`,
			`<title>` + tab + ` · Familiefoto&#39;s</title>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
		if strings.Contains(body, "· PhotoThing</title>") {
			t.Errorf("%s: the tab still ends in PhotoThing", path)
		}
		// the software keeps its own name
		if !strings.Contains(body, "<span>PhotoThing 0.0.0-dev</span>") || !strings.Contains(body, `<h2 id="about-title">PhotoThing</h2>`) {
			t.Errorf("%s: the footer or the About box lost the name of the program", path)
		}
	}
	// pages for visitors who are not signed in, and error pages, carry it too
	anon := e.client().mustGet("/login")
	if !strings.Contains(anon, `<span class="site">Familiefoto&#39;s</span>`) || !strings.Contains(anon, "<title>Sign in · Familiefoto&#39;s</title>") {
		t.Error("the sign-in page lacks the site title")
	}
	if code, body, _ := vera.get("/a/does-not-exist"); code != 404 || !strings.Contains(body, "· Familiefoto&#39;s</title>") {
		t.Errorf("the error page: %d", code)
	}
	// the title is not translated, and the rest of the header still is
	vera.post("/prefs", url.Values{"lang": {"nl"}, "next": {"/"}})
	if nl := vera.mustGet("/"); !strings.Contains(nl, "<title>Albums · Familiefoto&#39;s</title>") || !strings.Contains(nl, "Uitloggen") {
		t.Error("Dutch page")
	}
}

func TestNoSiteTitleLooksAsBefore(t *testing.T) {
	e := newEnv(t)
	body := e.login("vera").mustGet("/")
	for _, want := range []string{`<a class="brand" href="/">`, `<span class="product">PhotoThing</span>`, "<title>Albums · PhotoThing</title>"} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %s", want)
		}
	}
	for _, not := range []string{`class="site"`, `class="sep"`, "titled"} {
		if strings.Contains(body, not) {
			t.Errorf("a page without a site title contains %s", not)
		}
	}
}

// The title comes from the configuration, but it is still text: it is escaped in the header and in the tab.
func TestSiteTitleIsEscaped(t *testing.T) {
	evil := `</title><script>alert(1)</script> & "x"`
	e := newEnvWith(t, Options{SiteTitle: evil})
	for _, body := range []string{e.client().mustGet("/login"), e.login("vera").mustGet("/")} {
		if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, "</title><script") {
			t.Fatal("the site title was not escaped")
		}
		if !regexp.MustCompile(`<title>[^<]*· &lt;/title&gt;&lt;script&gt;alert\(1\)&lt;/script&gt; &amp; &#34;x&#34;</title>`).MatchString(body) {
			t.Error("the tab title is not the escaped text")
		}
	}
}
