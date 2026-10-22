package web

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"photothing/internal/auth"
	"photothing/internal/gallery"
	"photothing/internal/store"
)

const testPassword = "correct-horse-battery"

type env struct {
	t      *testing.T
	st     *store.Store
	lib    *gallery.Library
	srv    *Server
	ts     *httptest.Server
	photos string
	seq    int
}

func newEnv(t *testing.T) *env { return newEnvWith(t, Options{}) }

func newEnvWith(t *testing.T, opts Options) *env {
	t.Helper()
	dir := t.TempDir()
	photos := filepath.Join(dir, "photos")
	if err := os.MkdirAll(photos, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib, err := gallery.Open(photos, filepath.Join(dir, "cache"), st)
	if err != nil {
		t.Fatal(err)
	}
	box, err := auth.LoadBox(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, lib, box, opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"ton", "admin"}, {"ed", "editor"}, {"vera", "viewer"}} {
		if err := st.CreateUser(u[0], hash, u[1]); err != nil {
			t.Fatal(err)
		}
	}
	return &env{t: t, st: st, lib: lib, srv: srv, ts: ts, photos: photos}
}

// addPhoto writes a small JPEG at rel (root-relative) with a distinct size and modification time.
func (e *env) addPhoto(rel string) {
	e.t.Helper()
	e.seq++
	p := filepath.Join(e.photos, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, 32+e.seq, 24))
	for x := 0; x < 32+e.seq; x++ {
		img.Set(x, 3, color.RGBA{uint8(x), 80, 120, 255})
	}
	f, err := os.Create(p)
	if err != nil {
		e.t.Fatal(err)
	}
	jpeg.Encode(f, img, nil)
	f.Close()
	mt := time.Now().Add(-time.Duration(e.seq) * time.Hour)
	os.Chtimes(p, mt, mt)
}

func (e *env) scan() {
	e.t.Helper()
	if _, err := e.lib.Scan(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) photoID(rel string) int64 {
	e.t.Helper()
	idx, _ := e.st.PhotoIndex()
	ent, ok := idx[rel]
	if !ok {
		e.t.Fatalf("%s is not indexed", rel)
	}
	return ent.ID
}

type client struct {
	t    *testing.T
	c    *http.Client
	base string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.ts.URL, c: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (e *env) login(user string) *client {
	e.t.Helper()
	c := e.client()
	if code, _, _ := c.post("/login", url.Values{"username": {user}, "password": {testPassword}}); code != http.StatusSeeOther {
		e.t.Fatalf("login %s: status %d", user, code)
	}
	return c
}

func (c *client) do(req *http.Request) (int, string, http.Header) {
	c.t.Helper()
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (c *client) get(path string) (int, string, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	return c.do(req)
}

func (c *client) post(path string, v url.Values) (int, string, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest("POST", c.base+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req)
}

func (c *client) mustGet(path string) string {
	c.t.Helper()
	code, body, _ := c.get(path)
	if code != http.StatusOK {
		c.t.Fatalf("GET %s: status %d", path, code)
	}
	return body
}

func (c *client) mustPost(path string, v url.Values) string {
	c.t.Helper()
	code, body, _ := c.post(path, v)
	if code != http.StatusOK {
		c.t.Fatalf("POST %s: status %d\n%s", path, code, body)
	}
	return body
}

var altRe = regexp.MustCompile(`<li[^>]*><a href="/p/\d+"[^>]*><img[^>]*alt="([^"]*)"`)

func tileNames(html string) []string {
	var out []string
	for _, m := range altRe.FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// ---- tests ----

func TestAnonymousAndPublicMode(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("a/x.jpg")
	e.scan()
	anon := e.client()
	if code, _, h := anon.get("/"); code != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/login") {
		t.Errorf("anonymous gallery: %d %s", code, h.Get("Location"))
	}
	e.st.SetSetting("public", "1")
	if body := anon.mustGet("/"); !strings.Contains(body, `href="/a/a"`) {
		t.Error("public mode should list albums")
	}
	if code, _, _ := anon.get(fmt.Sprintf("/img/%d/original", e.photoID("a/x.jpg"))); code != http.StatusForbidden {
		t.Errorf("anonymous original: %d", code)
	}
}

func TestPaginationAndPersistedPageSize(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 30; i++ {
		e.addPhoto(fmt.Sprintf("big/img%02d.jpg", i))
	}
	e.scan()
	c := e.login("vera")
	if n := len(tileNames(c.mustGet("/a/big"))); n != 30 {
		t.Fatalf("default page size shows %d, want all 30", n)
	}
	// choose 12 per page (via the pager form)
	c.post("/prefs", url.Values{"page_size": {"12"}, "next": {"/a/big"}})
	p1 := c.mustGet("/a/big")
	if n := len(tileNames(p1)); n != 12 || !strings.Contains(p1, `href="/a/big?page=2"`) {
		t.Errorf("page 1: %d tiles", n)
	}
	if n := len(tileNames(c.mustGet("/a/big?page=3"))); n != 6 {
		t.Errorf("page 3: %d tiles, want 6", n)
	}
	if n := len(tileNames(c.mustGet("/a/big?page=99"))); n != 6 {
		t.Errorf("page beyond the end should clamp to the last page, got %d tiles", n)
	}
	if n := len(tileNames(c.mustGet("/a/big?page=abc"))); n != 12 {
		t.Errorf("bad page parameter: %d tiles", n)
	}
	// invalid sizes are ignored
	c.post("/prefs", url.Values{"page_size": {"7"}, "next": {"/"}})
	u, _ := e.st.UserByName("vera")
	if u.PageSize != 12 {
		t.Errorf("stored page size %d, want 12", u.PageSize)
	}
	// the preference is stored with the user, so a brand new session has it too
	if n := len(tileNames(e.login("vera").mustGet("/a/big"))); n != 12 {
		t.Errorf("new session: %d tiles, want 12", n)
	}
	// the photo page links back to the album page that contains the photo
	id := e.photoID("big/img20.jpg")
	if body := c.mustGet(fmt.Sprintf("/p/%d", id)); !strings.Contains(body, `href="/a/big?page=2"`) {
		t.Error("photo 20 of 30 at 12 per page belongs on page 2")
	}
}

func TestSortModes(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"d10.jpg", "d2.jpg", "B.jpg", "a.jpg"} {
		e.addPhoto("s/" + n) // later files get older mtimes, so date order is the reverse of creation
	}
	e.scan()
	c := e.login("vera")
	want := map[string][]string{
		"name":      {"a.jpg", "B.jpg", "d10.jpg", "d2.jpg"},
		"natural":   {"a.jpg", "B.jpg", "d2.jpg", "d10.jpg"},
		"date":      {"a.jpg", "B.jpg", "d2.jpg", "d10.jpg"}, // oldest (created last) first
		"date_desc": {"d10.jpg", "d2.jpg", "B.jpg", "a.jpg"},
	}
	for mode, names := range want {
		c.post("/prefs", url.Values{"sort": {mode}, "next": {"/"}})
		if got := tileNames(c.mustGet("/a/s")); !eq(got, names) {
			t.Errorf("sort %s: got %v, want %v", mode, got, names)
		}
	}
	// previous/next on the photo page follow the chosen order
	c.post("/prefs", url.Values{"sort": {"natural"}, "next": {"/"}})
	body := c.mustGet(fmt.Sprintf("/p/%d", e.photoID("s/d2.jpg")))
	if !strings.Contains(body, fmt.Sprintf(`data-next="/p/%d"`, e.photoID("s/d10.jpg"))) {
		t.Error("next photo after d2 should be d10 in natural order")
	}
}

func TestAlbumAccessRules(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("open/a.jpg")
	e.addPhoto("family/kids/b.jpg")
	e.addPhoto("family/c.jpg")
	e.scan()
	ed, _ := e.st.UserByName("ed")
	if err := e.st.SetAccess("family", []int64{ed.ID}); err != nil {
		t.Fatal(err)
	}
	secret := e.photoID("family/kids/b.jpg")

	vera := e.login("vera")
	if body := vera.mustGet("/"); strings.Contains(body, "family") || !strings.Contains(body, "open") {
		t.Error("viewer must not see the restricted album in the listing")
	}
	for _, p := range []string{"/a/family", "/a/family/kids", fmt.Sprintf("/p/%d", secret), fmt.Sprintf("/img/%d/thumb", secret), fmt.Sprintf("/img/%d/large", secret)} {
		if code, _, _ := vera.get(p); code != http.StatusNotFound {
			t.Errorf("viewer GET %s: %d, want 404", p, code)
		}
	}
	if body := vera.mustGet("/search?q=b.jpg"); len(tileNames(body)) != 0 {
		t.Error("search leaks a restricted photo")
	}
	for _, who := range []string{"ed", "ton"} {
		c := e.login(who)
		if !strings.Contains(c.mustGet("/"), "family") {
			t.Errorf("%s should see the album", who)
		}
		c.mustGet("/a/family/kids")
		c.mustGet(fmt.Sprintf("/img/%d/thumb", secret))
		if len(tileNames(c.mustGet("/search?q=b.jpg"))) != 1 {
			t.Errorf("%s should find the photo", who)
		}
	}
	// an explicit cover inside a restricted subtree must not show through on an open album
	e.st.SetCover("family", secret)
	if body := vera.mustGet("/"); strings.Contains(body, fmt.Sprintf("/img/%d/thumb", secret)) {
		t.Error("restricted cover leaked")
	}
	// anonymous visitors in public mode never get in
	e.st.SetSetting("public", "1")
	anon := e.client()
	if body := anon.mustGet("/"); strings.Contains(body, "family") {
		t.Error("anonymous visitor sees restricted album")
	}
	if code, _, h := anon.get("/a/family"); code != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/login") {
		t.Errorf("anonymous restricted album: %d %s", code, h.Get("Location"))
	}
	// removing the rule opens it again
	e.st.DeleteAccess("family")
	vera.mustGet("/a/family")
}

func TestAccessAdminUI(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("fam/a.jpg")
	e.scan()
	admin := e.login("ton")
	vera, _ := e.st.UserByName("vera")
	body := admin.mustPost("/admin/access", url.Values{"dir": {"fam"}, "user": {fmt.Sprint(vera.ID)}})
	if !strings.Contains(body, "fam") {
		t.Error("rule not listed")
	}
	e.login("vera").mustGet("/a/fam")
	admin.mustPost("/admin/access", url.Values{"dir": {"fam"}}) // nobody but admins
	if code, _, _ := e.login("vera").get("/a/fam"); code != http.StatusNotFound {
		t.Errorf("viewer after removal from the list: %d", code)
	}
	admin.mustPost("/admin/access/delete", url.Values{"dir": {"fam"}})
	e.login("vera").mustGet("/a/fam")
	if code, _, _ := e.login("ed").get("/admin/access"); code != http.StatusForbidden {
		t.Errorf("editor opened the access page: %d", code)
	}
}

func TestCaptionTagsAndSearch(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("trip/one.jpg")
	e.addPhoto("trip/two.jpg")
	e.scan()
	id := e.photoID("trip/one.jpg")
	ed := e.login("ed")
	body := ed.mustPost(fmt.Sprintf("/p/%d/meta", id), url.Values{"caption": {"Sunset at the harbour"}, "tags": {"#Sunset, harbour,sunset , evening"}})
	if !strings.Contains(body, "Sunset at the harbour") {
		t.Error("caption not shown")
	}
	tags, _ := e.st.PhotoTags(id)
	if len(tags) != 3 { // "Sunset" and "sunset" are the same tag
		t.Errorf("tags %v", tags)
	}

	vera := e.login("vera")
	if code, _, _ := vera.post(fmt.Sprintf("/p/%d/meta", id), url.Values{"caption": {"hacked"}}); code != http.StatusForbidden {
		t.Errorf("viewer edited a caption: %d", code)
	}
	for q, want := range map[string]int{"harbour": 1, "sunset": 1, "evening": 1, "one.jpg": 1, "harbour evening": 1,
		"trip":        2, // the album name matches both photos
		"harbour two": 0} {
		if got := tileNames(vera.mustGet("/search?q=" + url.QueryEscape(q))); len(got) != want {
			t.Errorf("search %q found %v, want %d result(s)", q, got, want)
		}
	}
	if got := tileNames(vera.mustGet("/search?q=nothing")); len(got) != 0 {
		t.Errorf("search nothing: %v", got)
	}
	if got := tileNames(vera.mustGet("/search?tag=evening")); len(got) != 1 {
		t.Errorf("tag filter: %v", got)
	}
	if got := tileNames(vera.mustGet("/search?q=%25")); len(got) != 0 { // LIKE wildcards are escaped
		t.Errorf("wildcard search matched %v", got)
	}
	if cloud := vera.mustGet("/search"); !strings.Contains(cloud, "/search?tag=harbour") {
		t.Error("tag cloud lacks the tag")
	}
	if page := vera.mustGet(fmt.Sprintf("/p/%d", id)); !strings.Contains(page, `href="/search?tag=evening"`) || strings.Contains(page, `name="caption"`) {
		t.Error("tags should link to search and viewers must not get the edit form")
	}
	if strings.Contains(ed.mustGet(fmt.Sprintf("/p/%d", id)), `name="caption"`) == false {
		t.Error("editor lacks the edit form")
	}
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, fmt.Sprintf("tag%d", i))
	}
	if body := ed.mustPostStatus(fmt.Sprintf("/p/%d/meta", id), url.Values{"tags": {strings.Join(many, ",")}}, http.StatusBadRequest); !strings.Contains(body, "Too many tags") {
		t.Error("too many tags accepted")
	}
}

func (c *client) mustPostStatus(path string, v url.Values, want int) string {
	c.t.Helper()
	code, body, _ := c.post(path, v)
	if code != want {
		c.t.Fatalf("POST %s: status %d, want %d", path, code, want)
	}
	return body
}

func TestLanguage(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("x.jpg")
	e.scan()

	// anonymous: Accept-Language, then the cookie
	anon := e.client()
	req, _ := http.NewRequest("GET", e.ts.URL+"/login", nil)
	req.Header.Set("Accept-Language", "nl-NL,nl;q=0.9,en;q=0.5")
	if _, body, _ := anon.do(req); !strings.Contains(body, "Inloggen") || !strings.Contains(body, `lang="nl"`) {
		t.Error("Accept-Language nl not honoured")
	}
	anon.post("/prefs", url.Values{"lang": {"en"}, "next": {"/login"}})
	req, _ = http.NewRequest("GET", e.ts.URL+"/login", nil)
	req.Header.Set("Accept-Language", "nl")
	if _, body, _ := anon.do(req); !strings.Contains(body, "Sign in") {
		t.Error("language cookie should win over Accept-Language")
	}
	if code, _, _ := anon.post("/prefs", url.Values{"lang": {"xx"}, "next": {"/login"}}); code != http.StatusSeeOther {
		t.Errorf("unknown language: %d", code)
	}

	// signed in: stored with the user and used by every later session
	c := e.login("vera")
	c.post("/prefs", url.Values{"lang": {"nl"}, "next": {"/"}})
	if body := c.mustGet("/"); !strings.Contains(body, "Uitloggen") || !strings.Contains(body, "Zoeken") {
		t.Error("interface not in Dutch")
	}
	if u, _ := e.st.UserByName("vera"); u.Language != "nl" {
		t.Errorf("stored language %q", u.Language)
	}
	fresh := e.login("vera")
	if body := fresh.mustGet("/account"); !strings.Contains(body, "Voorkeuren") {
		t.Error("new session does not use the saved language")
	}
	if code, body, _ := fresh.get("/nope-not-found"); code != http.StatusNotFound && !strings.Contains(body, "Terug") {
		t.Logf("404 page: %d", code)
	}
	// error messages are translated too
	if _, body, _ := fresh.get("/admin"); !strings.Contains(body, "Je hebt hier geen toestemming voor.") {
		t.Error("permission error not translated")
	}
}

func TestLoginThrottleDoesNotLockOutTheRealUser(t *testing.T) {
	e := newEnv(t)
	s := e.srv
	for i := 0; i < 5; i++ {
		s.failed("203.0.113.9", "vera") // an attacker guessing vera's password
	}
	if !s.throttled("203.0.113.9", "vera") {
		t.Error("the attacker should be blocked")
	}
	if s.throttled("198.51.100.4", "vera") {
		t.Error("vera, signing in from her own IP, must not be locked out by someone else's guesses")
	}
	// a distributed attack still hits the loose per-user limit
	for i := 0; i < 60; i++ {
		s.failed(fmt.Sprintf("192.0.2.%d", i), "ed")
	}
	if !s.throttled("198.51.100.4", "ed") {
		t.Error("a distributed attack on one account should eventually be slowed down")
	}
	// end to end: a locked client gets 429, with the translated message available
	c := e.client()
	for i := 0; i < 5; i++ {
		c.post("/login", url.Values{"username": {"ton"}, "password": {"wrong-password-xx"}})
	}
	if code, _, _ := c.post("/login", url.Values{"username": {"ton"}, "password": {testPassword}}); code != http.StatusTooManyRequests {
		t.Errorf("sixth attempt: %d, want 429", code)
	}
}

func TestRecoveryCodes(t *testing.T) {
	e := newEnv(t)
	c := e.login("vera")
	start := c.mustPost("/account/mfa/start", nil)
	secret := regexp.MustCompile(`<code>([A-Z2-7]{16,})</code>`).FindStringSubmatch(start)
	if secret == nil {
		t.Fatal("no secret on the setup page")
	}
	code, _ := totp.GenerateCode(secret[1], time.Now())
	done := c.mustPost("/account/mfa/confirm", url.Values{"code": {code}})
	codes := regexp.MustCompile(`<li><code>([a-z0-9]{5}-[a-z0-9]{5})</code></li>`).FindAllStringSubmatch(done, -1)
	if len(codes) != 10 {
		t.Fatalf("got %d recovery codes, want 10", len(codes))
	}
	if again := c.mustGet("/account"); regexp.MustCompile(`<li><code>[a-z0-9]{5}-`).MatchString(again) {
		t.Error("recovery codes must be shown only once")
	}

	signIn := func(secondFactor string) (int, *client) {
		cl := e.client()
		cl.post("/login", url.Values{"username": {"vera"}, "password": {testPassword}})
		code, _, _ := cl.post("/login/mfa", url.Values{"code": {secondFactor}, "next": {"/"}})
		return code, cl
	}
	if code, cl := signIn(strings.ToUpper(codes[0][1])); code != http.StatusSeeOther {
		t.Errorf("recovery code rejected: %d", code)
	} else if body := cl.mustGet("/account"); !strings.Contains(body, "Recovery codes left: 9.") {
		t.Error("used code not counted")
	}
	if code, _ := signIn(codes[0][1]); code != http.StatusUnauthorized {
		t.Errorf("recovery code worked twice: %d", code)
	}
	if code, _ := signIn("abcde-fghjk"); code != http.StatusUnauthorized {
		t.Errorf("made-up recovery code accepted: %d", code)
	}
	// regenerating invalidates the old set
	c2 := e.client()
	c2.post("/login", url.Values{"username": {"vera"}, "password": {testPassword}})
	other, _ := totp.GenerateCode(secret[1], time.Now().Add(30*time.Second))
	if code, _, _ := c2.post("/login/mfa", url.Values{"code": {other}}); code != http.StatusSeeOther {
		t.Fatalf("totp login: %d", code)
	}
	fresh := c2.mustPost("/account/mfa/recovery", url.Values{"current": {testPassword}})
	if regexp.MustCompile(`<li><code>` + codes[1][1]).MatchString(fresh) {
		t.Error("regenerated set contains an old code")
	}
	if code, _ := signIn(codes[1][1]); code != http.StatusUnauthorized {
		t.Errorf("old code still works after regeneration: %d", code)
	}
	// turning MFA off removes the codes
	c2.mustPost("/account/mfa/disable", url.Values{"current": {testPassword}})
	if n := e.st.CountRecoveryCodes(3); n != 0 {
		t.Errorf("%d recovery codes left after disabling MFA", n)
	}
}

func TestResetLink(t *testing.T) {
	e := newEnv(t)
	admin := e.login("ton")
	vera, _ := e.st.UserByName("vera")
	body := admin.mustPost(fmt.Sprintf("/admin/users/%d/reset-link", vera.ID), nil)
	m := regexp.MustCompile(`value="(http://[^"]+/reset\?t=([A-Za-z0-9_-]+))"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no reset link on the page")
	}
	token := m[2]
	old := e.login("vera") // an existing session must die when the password is reset

	anon := e.client()
	anon.mustGet("/reset?t=" + token)
	if code, _, _ := anon.post("/reset", url.Values{"t": {token}, "new": {"short"}, "confirm": {"short"}}); code != http.StatusBadRequest {
		t.Errorf("short password accepted: %d", code)
	}
	if code, _, _ := anon.post("/reset", url.Values{"t": {token}, "new": {"another-long-password-1"}, "confirm": {"different-long-password"}}); code != http.StatusBadRequest {
		t.Errorf("mismatch accepted: %d", code)
	}
	anon.mustPost("/reset", url.Values{"t": {token}, "new": {"another-long-password-1"}, "confirm": {"another-long-password-1"}})
	if code, _, _ := anon.get("/reset?t=" + token); code != http.StatusNotFound {
		t.Errorf("link usable twice: %d", code)
	}
	if code, _, _ := old.get("/"); code != http.StatusSeeOther {
		t.Errorf("old session survived the reset: %d", code)
	}
	if code, _, _ := e.client().post("/login", url.Values{"username": {"vera"}, "password": {testPassword}}); code != http.StatusUnauthorized {
		t.Errorf("old password still works: %d", code)
	}
	if code, _, _ := e.client().post("/login", url.Values{"username": {"vera"}, "password": {"another-long-password-1"}}); code != http.StatusSeeOther {
		t.Errorf("new password rejected: %d", code)
	}
	if code, _, _ := anon.get("/reset?t=garbage"); code != http.StatusNotFound {
		t.Errorf("garbage token: %d", code)
	}
	// expired links are refused
	tok2, h2 := auth.NewToken()
	e.st.CreateReset(h2, vera.ID, time.Now().Add(-time.Minute))
	if code, _, _ := anon.get("/reset?t=" + tok2); code != http.StatusNotFound {
		t.Errorf("expired link: %d", code)
	}
	if code, _, _ := e.login("ed").post(fmt.Sprintf("/admin/users/%d/reset-link", vera.ID), nil); code != http.StatusForbidden {
		t.Errorf("editor made a reset link: %d", code)
	}
}

func TestSessionsCanBeRevoked(t *testing.T) {
	e := newEnv(t)
	a, b := e.login("vera"), e.login("vera")
	page := a.mustGet("/account")
	handles := regexp.MustCompile(`/account/sessions/([0-9a-f]{16})/revoke`).FindAllStringSubmatch(page, -1)
	if len(handles) != 1 || !strings.Contains(page, "This session") {
		t.Fatalf("expected one revocable session besides the current one, got %d", len(handles))
	}
	b.mustGet("/")
	a.mustPost("/account/sessions/"+handles[0][1]+"/revoke", nil)
	if code, _, _ := b.get("/"); code != http.StatusSeeOther {
		t.Errorf("revoked session still works: %d", code)
	}
	a.mustGet("/") // the current one is untouched

	c := e.login("vera")
	a.mustPost("/account/sessions/revoke-others", nil)
	if code, _, _ := c.get("/"); code != http.StatusSeeOther {
		t.Errorf("revoke-others left a session: %d", code)
	}
	a.mustGet("/")
	// a user cannot revoke somebody else's session (ids: ton=1, ed=2, vera=3)
	other := e.login("ed")
	edSessions, _ := e.st.ListSessions(2)
	if ok, _ := e.st.DeleteSessionByPrefix(3, edSessions[0].TokenHash[:16]); ok {
		t.Error("deleted another user's session")
	}
	other.mustGet("/")
}

func TestAuditLog(t *testing.T) {
	e := newEnv(t)
	e.client().post("/login", url.Values{"username": {"vera"}, "password": {"wrong-wrong-wrong"}})
	admin := e.login("ton")
	admin.mustPost("/admin/users", url.Values{"username": {"newbie"}, "password": {"long-enough-password"}, "role": {"viewer"}})
	body := admin.mustGet("/admin/audit")
	for _, want := range []string{"login.fail", "login.ok", "user.create", "newbie"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit log lacks %q", want)
		}
	}
	if strings.Contains(body, "wrong-wrong-wrong") || strings.Contains(body, testPassword) {
		t.Error("a password ended up in the audit log")
	}
	if code, _, _ := e.login("ed").get("/admin/audit"); code != http.StatusForbidden {
		t.Errorf("editor read the audit log: %d", code)
	}
}

func TestGPSOnlyForThoseWhoMayDownloadOriginals(t *testing.T) {
	e := newEnv(t)
	e.addPhoto("g.jpg")
	e.scan()
	p, _ := e.st.PhotoByID(e.photoID("g.jpg"))
	p.Exif = map[string]string{"GPS": "52.37000, 4.89000", "Camera model": "X"}
	e.st.UpsertPhoto(p)
	page := fmt.Sprintf("/p/%d", p.ID)
	if body := e.login("vera").mustGet(page); strings.Contains(body, "52.37") || strings.Contains(body, "openstreetmap") {
		t.Error("viewer can see GPS coordinates")
	}
	if body := e.login("ed").mustGet(page); !strings.Contains(body, "https://www.openstreetmap.org/?mlat=52.37000") {
		t.Error("editor should see the GPS link")
	}
}

func TestVideoStreamsWithRanges(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t)
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10",
		"-pix_fmt", "yuv420p", filepath.Join(e.photos, "clip.mp4")).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create video: %v %s", err, out)
	}
	e.scan()
	id := e.photoID("clip.mp4")
	vera := e.login("vera")
	page := vera.mustGet(fmt.Sprintf("/p/%d", id))
	if !strings.Contains(page, "<video") || !strings.Contains(page, fmt.Sprintf("/img/%d/video", id)) {
		t.Error("photo page lacks a video player")
	}
	if strings.Contains(page, "/original") {
		t.Error("viewer is offered a download")
	}
	req, _ := http.NewRequest("GET", e.ts.URL+fmt.Sprintf("/img/%d/video", id), nil)
	req.Header.Set("Range", "bytes=0-99")
	code, body, h := vera.do(req)
	if code != http.StatusPartialContent || len(body) != 100 || h.Get("Content-Type") != "video/mp4" {
		t.Errorf("range request: %d, %d bytes, %s", code, len(body), h.Get("Content-Type"))
	}
	if code, _, _ := vera.get(fmt.Sprintf("/img/%d/thumb", id)); code != http.StatusOK {
		t.Errorf("video thumbnail: %d", code)
	}
	if code, _, _ := vera.get(fmt.Sprintf("/img/%d/original", id)); code != http.StatusForbidden {
		t.Errorf("viewer downloaded the original: %d", code)
	}
	// the stream route is for videos only
	e.addPhoto("still.jpg")
	e.scan()
	if code, _, _ := vera.get(fmt.Sprintf("/img/%d/video", e.photoID("still.jpg"))); code != http.StatusNotFound {
		t.Errorf("video route on a still image: %d", code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	_, _, h := e.client().get("/login")
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "media-src 'self'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff header")
	}
}

func TestSortHelpers(t *testing.T) {
	cases := [][3]string{{"a2", "a10", "<"}, {"a02", "a2", "="}, {"File9", "file10", "<"}, {"b", "a1", ">"}, {"x", "x1", "<"}, {"1", "01", "="}}
	for _, c := range cases {
		got := naturalCompare(c[0], c[1])
		want := map[string]int{"<": -1, "=": 0, ">": 1}[c[2]]
		if got != want {
			t.Errorf("naturalCompare(%q,%q)=%d, want %d", c[0], c[1], got, want)
		}
	}
	p := newPager(100, 10, 5, "/x?")
	if p.Pages != 10 || p.Prev != "/x?page=4" || p.Next != "/x?page=6" {
		t.Errorf("pager %+v", p)
	}
	if first := newPager(100, 10, 1, "/x?"); first.Prev != "" || first.Links[0].URL != "/x" {
		t.Errorf("first page %+v", first)
	}
	if empty := newPager(0, 10, 3, "/x?"); empty.Pages != 1 || empty.Page != 1 {
		t.Errorf("empty pager %+v", empty)
	}
}
