// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"photothing/internal/auth"
)

// Where a visitor lands after signing in comes from a form field that a link can pre-fill, so it must
// only ever be a path on this site. Browsers delete tabs and line breaks from URLs before they parse
// them, so "/<tab>/evil.example" would become "//evil.example", a link to another site.
func TestSafeNext(t *testing.T) {
	ok := []string{"/", "/a/b", "/a/b?page=2&x=y", "/p/3", "/search?q=a%20b", "/a/caf%C3%A9"}
	bad := []string{"", "//evil.example", "/\\evil.example", "/\t/evil.example", "/\n/evil.example", "/\r/evil.example",
		"/\x00/x", "/\x7f", "/\v/x", "/\f/x", "http://evil.example", "https://evil.example/", "javascript:alert(1)", "evil.example",
		"///evil.example", "/ \t/evil.example", "/ /x", "/a b\t"}
	for _, s := range ok {
		if got := safeNext(s); got != s {
			t.Errorf("safeNext(%q) = %q, want it unchanged", s, got)
		}
	}
	for _, s := range bad {
		if got := safeNext(s); got != "/" {
			t.Errorf("safeNext(%q) = %q, want \"/\"", s, got)
		}
	}
}

func TestLoginRedirectCannotLeaveTheSite(t *testing.T) {
	e := newEnv(t)
	for _, next := range []string{"/\t/evil.example", "//evil.example", "/\n/evil.example"} {
		c := e.client()
		code, _, h := c.post("/login", url.Values{"username": {"vera"}, "password": {testPassword}, "next": {next}})
		if code != http.StatusSeeOther || h.Get("Location") != "/" {
			t.Errorf("next=%q redirected to %q (%d)", next, h.Get("Location"), code)
		}
	}
	// the preference form uses the same field
	c := e.login("vera")
	if _, _, h := c.post("/prefs", url.Values{"lang": {"nl"}, "next": {"/\t/evil.example"}}); h.Get("Location") != "/" {
		t.Errorf("/prefs redirected to %q", h.Get("Location"))
	}
}

func (e *env) enableMFA(user string) string {
	e.t.Helper()
	c := e.login(user)
	start := c.mustPost("/account/mfa/start", nil)
	m := regexp.MustCompile(`<code>([A-Z2-7]{16,})</code>`).FindStringSubmatch(start)
	if m == nil {
		e.t.Fatal("no MFA secret on the setup page")
	}
	code, _ := totp.GenerateCode(m[1], time.Now())
	c.mustPost("/account/mfa/confirm", url.Values{"code": {code}})
	return m[1]
}

// A six-digit code can be guessed, so the number of wrong codes per user is capped no matter where they
// come from: someone who knows the password and has many addresses must not get unlimited tries.
func TestMFAGuessingIsCappedPerUser(t *testing.T) {
	e := newEnvWith(t, Options{TrustedProxies: mustTrusted(t, "127.0.0.1")}) // lets every attempt come from a new address
	secret := e.enableMFA("vera")

	attempt := func(from, code string) int {
		c := e.client()
		if got := login(c, "vera", testPassword, xff(from)); got != http.StatusSeeOther {
			t.Fatalf("password step from %s: %d", from, got)
		}
		status, _, _ := c.postWith("/login/mfa", url.Values{"code": {code}, "next": {"/"}}, xff(from))
		return status
	}
	for i := 0; i < 10; i++ {
		if got := attempt(fmt.Sprintf("203.0.113.%d", i+1), "000000"); got != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: %d", i+1, got)
		}
	}
	right, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if got := attempt("198.51.100.77", right); got != http.StatusTooManyRequests {
		t.Errorf("after 10 wrong codes even the right code is refused for a while, got %d", got)
	}
	// other accounts are not affected
	if got := login(e.client(), "ed", testPassword, xff("198.51.100.78")); got != http.StatusSeeOther {
		t.Errorf("another user cannot sign in: %d", got)
	}
}

func TestAuditLogNeverHoldsTypedPasswords(t *testing.T) {
	e := newEnv(t)
	typedPassword := "my-real-password-typed-in-the-name-field"
	c := e.client()
	login(c, typedPassword, "whatever-xyz", nil)
	login(c, "vera", "wrong-wrong-wrong", nil)
	login(c, "VERA", "wrong-wrong-wrong", nil) // the account's own spelling is recorded
	entries, _, _ := e.st.ListAudit(50, 0)
	var targets []string
	for _, en := range entries {
		if en.Action == "login.fail" {
			targets = append(targets, en.Target)
		}
		if strings.Contains(en.Target+en.Detail+en.Actor, typedPassword) {
			t.Errorf("the typed password reached the audit log: %+v", en)
		}
	}
	if strings.Join(targets, ",") != "vera,vera,(unknown user)" {
		t.Errorf("failed logins recorded as %v", targets)
	}
	// still true once the limit kicks in
	for i := 0; i < 6; i++ {
		login(c, typedPassword, "whatever-xyz", nil)
	}
	entries, _, _ = e.st.ListAudit(50, 0)
	for _, en := range entries {
		if strings.Contains(en.Target, typedPassword) {
			t.Errorf("the typed password reached the audit log (%s)", en.Action)
		}
	}
}

// A client that has been blocked can go on sending requests; the log mentions it once, not once per request.
func TestBlockedSignInsAreLoggedOncePerWindow(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for i := 0; i < 5; i++ {
		login(c, "ton", "wrong-wrong-wrong", nil)
	}
	for i := 0; i < 40; i++ {
		if code := login(c, "ton", "wrong-wrong-wrong", nil); code != http.StatusTooManyRequests {
			t.Fatalf("request %d after the limit: %d", i, code)
		}
	}
	if n := len(e.auditIPs("login.blocked")); n != 1 {
		t.Errorf("%d login.blocked rows for one blocked client, want 1", n)
	}
	if n := len(e.auditIPs("login.fail")); n != 5 {
		t.Errorf("%d login.fail rows, want the 5 real failures", n)
	}
}

func probeTags(t *testing.T, path string) map[string]string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags:stream_tags:format=duration", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var doc struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"duration": doc.Format.Duration}
	for k, v := range doc.Format.Tags {
		switch strings.ToLower(k) {
		case "major_brand", "minor_version", "compatible_brands", "encoder": // structure the muxer always writes
		default:
			all[strings.ToLower(k)] = v
		}
	}
	for _, s := range doc.Streams {
		for k, v := range s.Tags {
			if k != "handler_name" && k != "vendor_id" && k != "language" && k != "encoder" { // written by the muxer itself
				all["stream:"+strings.ToLower(k)] = v
			}
		}
	}
	return all
}

// A camera video carries the place it was filmed in its metadata. Users who may not download originals
// must not get that through the video player either.
func TestVideoShownToViewersHasNoLocation(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	e := newEnv(t)
	orig := filepath.Join(e.photos, "trip.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-pix_fmt", "yuv420p", "-shortest",
		"-metadata", "location=+52.3700+004.9000/", "-metadata", "title=Home sweet home", orig).CombinedOutput(); err != nil {
		t.Skipf("cannot create the video: %v %s", err, out)
	}
	if tags := probeTags(t, orig); tags["location"] == "" || tags["title"] == "" {
		t.Skipf("the test video carries no location, so there is nothing to prove: %v", tags)
	}
	e.scan()
	id := e.photoID("trip.mp4")
	path := fmt.Sprintf("/img/%d/video", id)

	fetch := func(c *client) string {
		t.Helper()
		code, body, h := c.get(path)
		if code != http.StatusOK || h.Get("Content-Type") != "video/mp4" {
			t.Fatalf("GET %s: %d %s", path, code, h.Get("Content-Type"))
		}
		f := filepath.Join(t.TempDir(), "got.mp4")
		os.WriteFile(f, []byte(body), 0o600)
		return f
	}

	e.st.SetSetting("public", "1")
	for who, c := range map[string]*client{"viewer": e.login("vera"), "anonymous visitor": e.client()} {
		tags := probeTags(t, fetch(c))
		for k, v := range tags {
			if k != "duration" {
				t.Errorf("%s gets metadata %s=%q", who, k, v)
			}
		}
		if d, _ := strconv.ParseFloat(tags["duration"], 64); d < 1.5 || d > 2.6 {
			t.Errorf("%s: the copy lasts %q seconds, the video 2", who, tags["duration"])
		}
		// still a normal, seekable stream: ranges work and the copy is not the original
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		req.Header.Set("Range", "bytes=0-99")
		if code, body, _ := c.do(req); code != http.StatusPartialContent || len(body) != 100 {
			t.Errorf("%s range request: %d, %d bytes", who, code, len(body))
		}
	}
	if code, _, _ := e.login("vera").get(fmt.Sprintf("/img/%d/original", id)); code != http.StatusForbidden {
		t.Errorf("viewer fetched the original: %d", code)
	}
	// someone who may download originals is given the file itself
	got := probeTags(t, fetch(e.login("ed")))
	if got["location"] == "" {
		t.Errorf("an editor should get the original, which has its metadata: %v", got)
	}
	// the copy is made once
	entries, _ := os.ReadDir(e.lib.CacheDir)
	copies := 0
	for _, en := range entries {
		if strings.Contains(en.Name(), "_vcopy_") {
			copies++
		}
		if strings.HasPrefix(en.Name(), "tmp-") {
			t.Errorf("leftover temporary file %s", en.Name())
		}
	}
	if copies != 1 {
		t.Errorf("%d cached copies, want 1", copies)
	}
	// the cache is cleaned up like the rest when the video disappears
	os.Remove(orig)
	e.scan()
	e.lib.PruneCache()
	entries, _ = os.ReadDir(e.lib.CacheDir)
	for _, en := range entries {
		if strings.Contains(en.Name(), "_vcopy_") {
			t.Errorf("%s was not pruned", en.Name())
		}
	}
}

func TestThrottleAddr(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "::ffff:203.0.113.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"not-an-address":       "not-an-address",
	} {
		if got := throttleAddr(in); got != want {
			t.Errorf("throttleAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// One IPv6 customer owns a whole /64: stepping from address to address inside it must not reset the limit.
func TestIPv6BlockSharesOneLimit(t *testing.T) {
	e := newEnvWith(t, Options{TrustedProxies: mustTrusted(t, "127.0.0.1")})
	c := e.client()
	for i := 0; i < 5; i++ {
		login(c, "ton", "wrong-wrong-wrong", xff(fmt.Sprintf("2001:db8:aaaa:bbbb::%x", i+1)))
	}
	if code := login(c, "ton", testPassword, xff("2001:db8:aaaa:bbbb:dead:beef::1")); code != http.StatusTooManyRequests {
		t.Errorf("a new address in the same /64 dodged the limit: %d", code)
	}
	if code := login(e.client(), "ton", testPassword, xff("2001:db8:aaaa:cccc::1")); code != http.StatusSeeOther {
		t.Errorf("a different /64 is blocked too: %d", code)
	}
}

// In public mode an anonymous visitor must not learn which photo numbers exist or are hidden.
func TestAnonymousCannotProbePhotoNumbers(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("open/a.jpg")
	e.addPhoto("hidden/b.jpg")
	e.scan()
	ed, _ := e.st.UserByName("ed")
	e.st.SetAccess("hidden", []int64{ed.ID})
	e.st.SetSetting("public", "1")
	anon := e.client()
	answer := func(path string) string {
		code, _, h := anon.get(path)
		loc := h.Get("Location")
		if i := strings.Index(loc, "?"); i >= 0 {
			loc = loc[:i]
		}
		return fmt.Sprintf("%d %s", code, loc)
	}
	real, hidden, missing := e.photoID("open/a.jpg"), e.photoID("hidden/b.jpg"), int64(99999)
	for _, kind := range []string{"/p/%d", "/img/%d/thumb", "/img/%d/large"} {
		got := map[string]string{"hidden": answer(fmt.Sprintf(kind, hidden)), "missing": answer(fmt.Sprintf(kind, missing)), "junk": answer(strings.Replace(kind, "%d", "abc", 1))}
		if got["hidden"] != got["missing"] || got["hidden"] != got["junk"] {
			t.Errorf("%s tells hidden, missing and invalid numbers apart: %v", kind, got)
		}
		if ok := answer(fmt.Sprintf(kind, real)); strings.HasPrefix(ok, "303") {
			t.Errorf("%s: a public photo is not shown: %s", kind, ok)
		}
	}
}

// Recovery codes issued before they were keyed are stored unkeyed; they keep working, once.
func TestRecoveryCodesIssuedBeforeKeyingStillWork(t *testing.T) {
	e := newEnv(t)
	e.enableMFA("vera")
	vera, _ := e.st.UserByName("vera")
	if err := e.st.ReplaceRecoveryCodes(vera.ID, []string{auth.HashRecovery("abcdefghjk")}); err != nil {
		t.Fatal(err)
	}
	signIn := func() int {
		c := e.client()
		login(c, "vera", testPassword, nil)
		code, _, _ := c.post("/login/mfa", url.Values{"code": {"ABCDE-FGHJK"}, "next": {"/"}})
		return code
	}
	if code := signIn(); code != http.StatusSeeOther {
		t.Errorf("an old-style recovery code was refused: %d", code)
	}
	if code := signIn(); code != http.StatusUnauthorized {
		t.Errorf("an old-style recovery code worked twice: %d", code)
	}
}
