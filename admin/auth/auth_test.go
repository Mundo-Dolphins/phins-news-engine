package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSessions(t *testing.T, ttl time.Duration) (*auth.Sessions, *clock) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := &clock{t: t0}
	return auth.NewSessions(st, ttl, c.now), c
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFrom(r.Context())
		_, _ = w.Write([]byte("hello " + u))
	})
}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := auth.HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(h, "s3cret") || auth.CheckPassword(h, "nope") || auth.CheckPassword("garbage", "s3cret") {
		t.Fatal("password check wrong")
	}
}

func login(t *testing.T, s *auth.Sessions) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Login(context.Background(), rec, "admin"); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %d", len(cs))
	}
	return cs[0]
}

func TestLoginCookieAttributes(t *testing.T) {
	s, _ := newSessions(t, 12*time.Hour)
	c := login(t, s)
	if c.Name != "phins_admin_session" || c.Path != "/" || !c.HttpOnly || c.Secure ||
		c.SameSite != http.SameSiteStrictMode || c.MaxAge != 43200 {
		t.Fatalf("cookie = %+v", c)
	}
	if len(c.Value) != 43 || strings.ContainsAny(c.Value, "=+/") {
		t.Fatalf("token = %q", c.Value)
	}
}

func TestMiddlewareAcceptsAndSlides(t *testing.T) {
	s, clk := newSessions(t, time.Hour)
	c := login(t, s)
	h := s.Middleware(okHandler())

	clk.t = t0.Add(50 * time.Minute)
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "hello admin" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].Value != c.Value || cs[0].MaxAge != 3600 {
		t.Fatalf("cookie not refreshed: %+v", cs)
	}

	// 50m after login would be 100m: only alive because expiry slid.
	clk.t = t0.Add(100 * time.Minute)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("sliding expiry failed: %d", rec.Code)
	}
}

func TestMiddlewareRejects(t *testing.T) {
	s, clk := newSessions(t, time.Hour)
	c := login(t, s)
	h := s.Middleware(okHandler())

	check := func(name string, req *http.Request) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := `{"error":{"code":"unauthorized","message":"login required"}}`
		if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != want ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %q %q", name, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
	}
	check("no cookie", httptest.NewRequest("GET", "/api/x", nil))
	bad := httptest.NewRequest("GET", "/api/x", nil)
	bad.AddCookie(&http.Cookie{Name: "phins_admin_session", Value: "bogus"})
	check("bogus", bad)

	clk.t = t0.Add(2 * time.Hour)
	exp := httptest.NewRequest("GET", "/api/x", nil)
	exp.AddCookie(c)
	check("expired", exp)
}

func TestLogout(t *testing.T) {
	s, _ := newSessions(t, time.Hour)
	c := login(t, s)
	req := httptest.NewRequest("POST", "/api/logout", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	if err := s.Logout(context.Background(), rec, req); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].MaxAge != -1 || cs[0].Name != "phins_admin_session" {
		t.Fatalf("clear cookie = %+v", cs)
	}
	rec = httptest.NewRecorder()
	s.Middleware(okHandler()).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("after logout = %d", rec.Code)
	}
	// Logout without a cookie is harmless.
	if err := s.Logout(context.Background(), httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLimiter(t *testing.T) {
	c := &clock{t: t0}
	l := auth.NewLoginLimiter(5, time.Minute, c.now)
	ip := netip.MustParseAddr("192.168.1.9")
	other := netip.MustParseAddr("192.168.1.10")
	for i := 0; i < 5; i++ {
		if !l.Allow(ip) {
			t.Fatalf("blocked early at %d", i)
		}
		l.Fail(ip)
	}
	if l.Allow(ip) {
		t.Fatal("6th attempt should be blocked")
	}
	if !l.Allow(other) {
		t.Fatal("other IP affected")
	}
	c.t = t0.Add(61 * time.Second)
	if !l.Allow(ip) {
		t.Fatal("should be allowed after window")
	}
	for i := 0; i < 5; i++ {
		l.Fail(ip)
	}
	if l.Allow(ip) {
		t.Fatal("should block again")
	}
	l.Reset(ip)
	if !l.Allow(ip) {
		t.Fatal("reset should unblock")
	}
}

func prefixes(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func lanStatus(allowed []netip.Prefix, remote string, hdr map[string]string) int {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	auth.LANOnly(allowed, okHandler()).ServeHTTP(rec, req)
	return rec.Code
}

func TestLANOnlyIgnoresForwardedFor(t *testing.T) {
	a := prefixes(t, "192.168.0.0/16")
	got := lanStatus(a, "203.0.113.7:5555", map[string]string{"X-Forwarded-For": "192.168.1.10", "X-Real-IP": "192.168.1.10"})
	if got != 403 {
		t.Fatalf("status = %d", got)
	}
}

func TestLANOnlyAddresses(t *testing.T) {
	a := prefixes(t, "192.168.0.0/16", "::1/128", "127.0.0.0/8")
	cases := []struct {
		remote string
		want   int
	}{
		{"192.168.1.5:1", 200},
		{"[::1]:1", 200},
		{"[::ffff:192.168.1.5]:1", 200},
		{"[::ffff:8.8.8.8]:1", 403},
		{"10.0.0.1:1", 403},
		{"garbage", 403},
		{"", 403},
	}
	for _, c := range cases {
		if got := lanStatus(a, c.remote, nil); got != c.want {
			t.Errorf("%q = %d want %d", c.remote, got, c.want)
		}
	}
	if got := lanStatus(nil, "127.0.0.1:1", nil); got != 403 {
		t.Errorf("empty list = %d, want 403", got)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "8.8.8.8:1"
	rec := httptest.NewRecorder()
	auth.LANOnly(a, okHandler()).ServeHTTP(rec, req)
	if strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"forbidden","message":"address not allowed"}}` ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::ffff:10.1.2.3]:80"
	ip, ok := auth.ClientIP(req)
	if !ok || ip != netip.MustParseAddr("10.1.2.3") {
		t.Fatalf("ip = %v %v", ip, ok)
	}
	req.RemoteAddr = "nope"
	if _, ok := auth.ClientIP(req); ok {
		t.Fatal("expected !ok")
	}
}

func TestSameOrigin(t *testing.T) {
	h := auth.SameOrigin(okHandler())
	do := func(method string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://panel.lan:8480/api/x", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		want   int
	}{
		{"GET no headers", "GET", nil, 200},
		{"HEAD no headers", "HEAD", nil, 200},
		{"GET cross origin ok", "GET", map[string]string{"Origin": "http://evil.example"}, 200},
		{"POST same origin", "POST", map[string]string{"Origin": "http://panel.lan:8480"}, 200},
		{"POST cross origin", "POST", map[string]string{"Origin": "http://evil.example"}, 403},
		{"POST other port", "POST", map[string]string{"Origin": "http://panel.lan:9999"}, 403},
		{"POST null origin", "POST", map[string]string{"Origin": "null"}, 403},
		{"POST referer same", "POST", map[string]string{"Referer": "http://panel.lan:8480/ui"}, 200},
		{"POST referer cross", "POST", map[string]string{"Referer": "http://evil.example/x"}, 403},
		{"POST neither", "POST", nil, 403},
		{"DELETE cross", "DELETE", map[string]string{"Origin": "http://evil.example"}, 403},
		{"POST origin wins over referer", "POST", map[string]string{"Origin": "http://evil.example", "Referer": "http://panel.lan:8480/"}, 403},
	}
	for _, c := range cases {
		rec := do(c.method, c.hdr)
		if rec.Code != c.want {
			t.Errorf("%s = %d want %d", c.name, rec.Code, c.want)
		}
		if c.want == 403 && strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"forbidden","message":"cross-origin request refused"}}` {
			t.Errorf("%s body = %q", c.name, rec.Body.String())
		}
	}
}

func TestBearer(t *testing.T) {
	h := auth.Bearer("tok123", okHandler())
	do := func(authz string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/mcp", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("Bearer tok123"); rec.Code != 200 {
		t.Fatalf("good = %d", rec.Code)
	}
	for _, a := range []string{"", "Bearer wrong", "Bearer ", "tok123", "Basic tok123", "Bearer tok1234"} {
		rec := do(a)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" ||
			strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"unauthorized","message":"invalid or missing bearer token"}}` {
			t.Errorf("%q = %d %q", a, rec.Code, rec.Body.String())
		}
	}
	// Empty configured token never authenticates.
	empty := auth.Bearer("", okHandler())
	for _, a := range []string{"", "Bearer ", "Bearer x"} {
		req := httptest.NewRequest("POST", "/mcp", nil)
		if a != "" {
			req.Header.Set("Authorization", a)
		}
		rec := httptest.NewRecorder()
		empty.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("empty token %q = %d", a, rec.Code)
		}
	}
}
