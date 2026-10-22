package web

import (
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func mustTrusted(t *testing.T, list string) []netip.Prefix {
	t.Helper()
	p, err := ParseTrustedProxies(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseTrustedProxies(t *testing.T) {
	got := mustTrusted(t, " 127.0.0.1, 10.1.2.3/8 ,::1,::ffff:192.0.2.1, ")
	want := []string{"127.0.0.1/32", "10.0.0.0/8", "::1/128", "192.0.2.1/32"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("entry %d = %s, want %s", i, got[i], w)
		}
	}
	if got := mustTrusted(t, ""); len(got) != 0 {
		t.Errorf("empty list gave %v", got)
	}
	for _, bad := range []string{"nginx", "10.0.0.0/33", "1.2.3", "127.0.0.1;10.0.0.1"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestRealIP(t *testing.T) {
	trusted := mustTrusted(t, "127.0.0.1,10.0.0.0/8")
	peer := netip.MustParseAddr("127.0.0.1")
	cases := []struct {
		name string
		xff  []string
		xri  string
		want string
	}{
		{"single hop", []string{"203.0.113.7"}, "", "203.0.113.7"},
		{"visitor-supplied prefix is not believed", []string{"1.2.3.4, 203.0.113.7"}, "", "203.0.113.7"},
		{"trusted hops on the right are skipped", []string{"203.0.113.7, 10.1.1.1"}, "", "203.0.113.7"},
		{"visitor spoofs a trusted address on the left", []string{"10.0.0.99, 203.0.113.7"}, "", "203.0.113.7"},
		{"all hops trusted", []string{"10.0.0.5, 10.1.1.1"}, "", "10.0.0.5"},
		{"garbage header falls back to the proxy", []string{"not-an-ip"}, "", "127.0.0.1"},
		{"garbage on the right falls back to the proxy", []string{"203.0.113.7, garbage"}, "", "127.0.0.1"},
		{"no forwarded header uses X-Real-IP", nil, "198.51.100.2", "198.51.100.2"},
		{"forwarded header wins over X-Real-IP", []string{"203.0.113.7"}, "198.51.100.2", "203.0.113.7"},
		{"nothing at all", nil, "", "127.0.0.1"},
		{"ipv6", []string{"2001:db8::1"}, "", "2001:db8::1"},
		{"port is dropped", []string{"203.0.113.7:5555"}, "", "203.0.113.7"},
		{"ipv6 with port", []string{"[2001:db8::1]:443"}, "", "2001:db8::1"},
		{"ipv4 mapped in ipv6", []string{"::ffff:203.0.113.7"}, "", "203.0.113.7"},
		{"several header lines", []string{"1.2.3.4", "203.0.113.7"}, "", "203.0.113.7"},
		{"empty header", []string{""}, "", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := realIP(peer, c.xff, c.xri, trusted); got.String() != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func (c *client) postWith(path string, v url.Values, hdr map[string]string) (int, string, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest("POST", c.base+path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	return c.do(req)
}

func (e *env) auditIPs(action string) []string {
	e.t.Helper()
	entries, _, err := e.st.ListAudit(100, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		if en.Action == action {
			out = append(out, en.IP)
		}
	}
	return out
}

func xff(v string) map[string]string { return map[string]string{"X-Forwarded-For": v} }

func login(c *client, user, pw string, h map[string]string) int {
	code, _, _ := c.postWith("/login", url.Values{"username": {user}, "password": {pw}}, h)
	return code
}

// The core promise: a proxy we trust tells us who the visitor is, and visitors are limited separately.
func TestTrustedProxyIdentifiesVisitors(t *testing.T) {
	e := newEnvWith(t, Options{TrustedProxies: mustTrusted(t, "127.0.0.1")})
	c := e.client()
	attacker := xff("203.0.113.7")
	for i := 0; i < 5; i++ {
		if code := login(c, "ton", "wrong-password-xyz", attacker); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := login(c, "ton", testPassword, attacker); code != http.StatusTooManyRequests {
		t.Errorf("the guessing visitor should be blocked, got %d", code)
	}
	// Another visitor, arriving through the same proxy connection, is not affected.
	if code := login(e.client(), "ton", testPassword, xff("198.51.100.4")); code != http.StatusSeeOther {
		t.Errorf("an innocent visitor is blocked by someone else's failures: %d", code)
	}
	// The failures were recorded against the visitor, not the proxy.
	for _, ip := range e.auditIPs("login.fail") {
		if ip != "203.0.113.7" {
			t.Errorf("audit log recorded %s", ip)
		}
	}
	if ips := e.auditIPs("login.ok"); len(ips) != 1 || ips[0] != "198.51.100.4" {
		t.Errorf("successful login recorded as %v", ips)
	}
	// Addresses the visitor added themselves in front of the real one are not believed.
	e2 := newEnvWith(t, Options{TrustedProxies: mustTrusted(t, "127.0.0.1")})
	login(e2.client(), "ton", "wrong-password-xyz", xff("1.2.3.4, 203.0.113.9"))
	if ips := e2.auditIPs("login.fail"); len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Errorf("got %v, want the right-most untrusted address", ips)
	}
	// The sessions list shows the visitor's address too.
	good := e.client()
	good.postWith("/login", url.Values{"username": {"vera"}, "password": {testPassword}}, xff("198.51.100.77"))
	if ss, _ := e.st.ListSessions(3); len(ss) != 1 || ss[0].IP != "198.51.100.77" {
		t.Errorf("session recorded %+v", ss)
	}
}

// The security-critical case: without a trusted proxy, forwarded headers must change nothing.
func TestForwardedHeadersAreIgnoredWithoutATrustedProxy(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	// Making up a new address for every attempt must not dodge the limit.
	for i := 0; i < 5; i++ {
		login(c, "ton", "wrong-password-xyz", xff(fmt.Sprintf("203.0.113.%d", i+10)))
	}
	if code := login(c, "ton", testPassword, xff("198.51.100.200")); code != http.StatusTooManyRequests {
		t.Errorf("spoofed X-Forwarded-For dodged the login limit: %d", code)
	}
	for _, ip := range e.auditIPs("login.fail") {
		if ip != "127.0.0.1" {
			t.Errorf("audit log believed a forged address: %s", ip)
		}
	}
	// X-Real-IP and X-Forwarded-Proto are ignored as well.
	e2 := newEnv(t)
	code, _, h := e2.client().postWith("/login", url.Values{"username": {"vera"}, "password": {testPassword}},
		map[string]string{"X-Real-IP": "203.0.113.50", "X-Forwarded-Proto": "https"})
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	if strings.Contains(strings.Join(h.Values("Set-Cookie"), ";"), "Secure") {
		t.Error("untrusted X-Forwarded-Proto made the cookie Secure")
	}
	if ss, _ := e2.st.ListSessions(3); len(ss) != 1 || ss[0].IP != "127.0.0.1" {
		t.Errorf("session recorded %+v", ss)
	}
}

// A proxy that is trusted for some addresses only: connections from elsewhere are not believed.
func TestOnlyTheListedProxyIsBelieved(t *testing.T) {
	e := newEnvWith(t, Options{TrustedProxies: mustTrusted(t, "192.0.2.0/24")}) // the test client connects from 127.0.0.1
	login(e.client(), "ton", "wrong-password-xyz", xff("203.0.113.7"))
	if ips := e.auditIPs("login.fail"); len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Errorf("got %v: a connection from an unlisted address was believed", ips)
	}
}

func TestSecureCookiesFollowTheProxyScheme(t *testing.T) {
	trusted := Options{TrustedProxies: mustTrusted(t, "127.0.0.1")}
	secure := func(e *env, hdr map[string]string) bool {
		_, _, h := e.client().postWith("/login", url.Values{"username": {"vera"}, "password": {testPassword}}, hdr)
		return strings.Contains(strings.Join(h.Values("Set-Cookie"), ";"), "Secure")
	}
	e := newEnvWith(t, trusted)
	if !secure(e, map[string]string{"X-Forwarded-Proto": "https"}) {
		t.Error("https behind a trusted proxy should give a Secure cookie")
	}
	if secure(e, map[string]string{"X-Forwarded-Proto": "http"}) || secure(e, nil) {
		t.Error("plain http must not give a Secure cookie")
	}
	if !secure(newEnvWith(t, Options{SecureCookies: true}), nil) {
		t.Error("-secure-cookies must always set the flag")
	}

	// reset links use the scheme the admin is browsing with
	admin := e.client()
	admin.postWith("/login", url.Values{"username": {"ton"}, "password": {testPassword}}, nil)
	link := func(proto string) string {
		_, body, _ := admin.postWith("/admin/users/3/reset-link", nil, map[string]string{"X-Forwarded-Proto": proto})
		m := regexp.MustCompile(`value="(https?)://`).FindStringSubmatch(body)
		if m == nil {
			t.Fatal("no link in the page")
		}
		return m[1]
	}
	if got := link("https"); got != "https" {
		t.Errorf("link scheme %s behind an https proxy", got)
	}
	if got := link("http"); got != "http" {
		t.Errorf("link scheme %s behind an http proxy", got)
	}
}
