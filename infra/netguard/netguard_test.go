package netguard_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

const secret = "SECRET-TOKEN-123"

func TestReadBodyWithinLimit(t *testing.T) {
	data, err := netguard.ReadBody(strings.NewReader("hello"), 5)
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadBody = %q, %v", data, err)
	}
}

func TestReadBodyRejectsOversizedBody(t *testing.T) {
	_, err := netguard.ReadBody(bytes.NewReader(make([]byte, 11)), 10)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if !strings.Contains(err.Error(), "response exceeds 10 bytes") {
		t.Fatalf("err = %q, want the limit in the message", err)
	}
}

func TestReadBodyNonPositiveLimitUsesJSONLimit(t *testing.T) {
	_, err := netguard.ReadBody(bytes.NewReader(make([]byte, netguard.JSONBodyLimit+1)), 0)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge for a zero limit", err)
	}
}

func TestTimeoutOr(t *testing.T) {
	if got := netguard.TimeoutOr(0, time.Second); got != time.Second {
		t.Fatalf("TimeoutOr(0) = %v", got)
	}
	if got := netguard.TimeoutOr(-1, time.Second); got != time.Second {
		t.Fatalf("TimeoutOr(-1) = %v", got)
	}
	if got := netguard.TimeoutOr(3*time.Second, time.Second); got != 3*time.Second {
		t.Fatalf("TimeoutOr(3s) = %v", got)
	}
}

func mustReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestCheckRedirectCapsAtFive(t *testing.T) {
	via := []*http.Request{mustReq(t, "https://a.example.com/")}
	for i := 0; i < netguard.MaxRedirects; i++ {
		if err := netguard.CheckRedirect(mustReq(t, "https://a.example.com/next"), via); err != nil {
			t.Fatalf("redirect %d refused: %v", i+1, err)
		}
		via = append(via, mustReq(t, "https://a.example.com/"))
	}
	if err := netguard.CheckRedirect(mustReq(t, "https://a.example.com/next"), via); err == nil {
		t.Fatal("redirect 6 accepted, want refusal")
	}
}

func TestCheckRedirectRefusesHTTPSDowngrade(t *testing.T) {
	via := []*http.Request{mustReq(t, "https://a.example.com/")}
	err := netguard.CheckRedirect(mustReq(t, "http://a.example.com/"), via)
	if err == nil || !strings.Contains(err.Error(), "https to http") {
		t.Fatalf("err = %v, want downgrade refusal", err)
	}
	if err := netguard.CheckRedirect(mustReq(t, "https://b.example.com/"), via); err != nil {
		t.Fatalf("https->https refused: %v", err)
	}
	via = []*http.Request{mustReq(t, "http://a.example.com/")}
	if err := netguard.CheckRedirect(mustReq(t, "https://a.example.com/"), via); err != nil {
		t.Fatalf("http->https refused: %v", err)
	}
}

func TestClientRefusesRealHTTPSDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("plain-http server must never be reached")
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/?token="+secret, http.StatusFound)
	}))
	defer tlsSrv.Close()

	tr := netguard.NewTransport(netguard.AllowPrivate)
	srvTransport, ok := tlsSrv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected test server transport type")
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: srvTransport.TLSClientConfig.RootCAs, MinVersion: tls.VersionTLS12}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr, CheckRedirect: netguard.CheckRedirect}
	resp, err := client.Get(tlsSrv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("downgrade redirect followed")
	}
	if strings.Contains(netguard.RedactError(err).Error(), secret) {
		t.Fatalf("redacted error leaks the secret: %v", netguard.RedactError(err))
	}
}

func TestClientStopsRedirectLoop(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: netguard.NewTransport(netguard.AllowPrivate), CheckRedirect: netguard.CheckRedirect}
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect loop did not fail")
	}
	if got := hits.Load(); got != netguard.MaxRedirects+1 {
		t.Fatalf("server hits = %d, want %d", got, netguard.MaxRedirects+1)
	}
}

func TestValidateURL(t *testing.T) {
	cases := []struct {
		raw    string
		policy netguard.HostPolicy
		ok     bool
	}{
		{"https://feeds.example.com/rss", netguard.BlockPrivate, true},
		{"http://feeds.example.com:8080/rss?x=1", netguard.BlockPrivate, true},
		{"ftp://feeds.example.com/rss", netguard.AllowPrivate, false},
		{"file:///etc/passwd", netguard.AllowPrivate, false},
		{"https://user:pass@feeds.example.com/", netguard.AllowPrivate, false},
		{"https:///no-host", netguard.AllowPrivate, false},
		{"://bad", netguard.AllowPrivate, false},
		{"http://169.254.169.254/", netguard.BlockPrivate, false},
		{"http://169.254.169.254/", netguard.AllowPrivate, false},
		{"http://[fe80::1]/", netguard.AllowPrivate, false},
		{"http://0.0.0.0/", netguard.AllowPrivate, false},
		{"http://127.0.0.1:8080/", netguard.BlockPrivate, false},
		{"http://127.0.0.1:8080/", netguard.AllowPrivate, true},
		{"http://[::1]/", netguard.BlockPrivate, false},
		{"http://10.1.2.3/", netguard.BlockPrivate, false},
		{"http://172.16.0.1/", netguard.BlockPrivate, false},
		{"http://192.168.1.1/", netguard.BlockPrivate, false},
		{"http://192.168.1.1/", netguard.AllowPrivate, true},
		{"http://100.64.0.1/", netguard.BlockPrivate, false},
		{"http://[fd00::1]/", netguard.BlockPrivate, false},
		{"http://[::ffff:127.0.0.1]/", netguard.BlockPrivate, false},
		{"http://224.0.0.1/", netguard.AllowPrivate, false},
		{"http://localhost/", netguard.BlockPrivate, false},
		{"http://localhost:3000/", netguard.AllowPrivate, true},
	}
	for _, tc := range cases {
		_, err := netguard.ValidateURL(tc.raw, tc.policy)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateURL(%q, %v) err = %v, want ok=%v", tc.raw, tc.policy, err, tc.ok)
		}
		if err != nil && !errors.Is(err, netguard.ErrDisallowedURL) {
			t.Errorf("ValidateURL(%q) err = %v, want ErrDisallowedURL", tc.raw, err)
		}
	}
}

func TestValidateURLErrorNeverContainsSecret(t *testing.T) {
	for _, raw := range []string{
		"ftp://h.example.com/?token=" + secret,
		"https://u:" + secret + "@h.example.com/",
		"http://169.254.169.254/?token=" + secret,
		"http://h.example.com/%zz?token=" + secret,
	} {
		_, err := netguard.ValidateURL(raw, netguard.BlockPrivate)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("ValidateURL(%q) err = %v, must be non-nil and secret-free", raw, err)
		}
	}
}

func TestBlockPrivateTransportRejectsLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("blocked host was reached")
	}))
	defer srv.Close()
	// "localhost" passes the literal-IP check; only the dial-time guard can
	// see it resolves to loopback (the DNS-rebinding case).
	u, _ := url.Parse(srv.URL)
	target := "http://localhost:" + u.Port() + "/"
	client := &http.Client{Timeout: 5 * time.Second, Transport: netguard.NewTransport(netguard.BlockPrivate)}
	resp, err := client.Get(target)
	if err == nil {
		resp.Body.Close()
		t.Fatal("loopback dial succeeded under BlockPrivate")
	}
	if !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

func TestAllowPrivateTransportStillBlocksLinkLocal(t *testing.T) {
	tr := netguard.NewTransport(netguard.AllowPrivate)
	conn, err := tr.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		conn.Close()
		t.Fatal("link-local dial allowed")
	}
	if !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

// Cloud metadata endpoints outside link-local space: AWS IPv6
// (fd00:ec2::254, inside ULA) and Alibaba (100.100.100.200, inside CGNAT).
// Both are refused under either policy, on the URL and at dial time.
func TestMetadataAddressesBlockedUnderBothPolicies(t *testing.T) {
	for _, policy := range []netguard.HostPolicy{netguard.BlockPrivate, netguard.AllowPrivate} {
		for _, raw := range []string{"http://[fd00:ec2::254]/latest/", "http://100.100.100.200/latest/meta-data/"} {
			if _, err := netguard.ValidateURL(raw, policy); !errors.Is(err, netguard.ErrDisallowedURL) {
				t.Errorf("ValidateURL(%q, %v) err = %v, want ErrDisallowedURL", raw, policy, err)
			}
		}
		tr := netguard.NewTransport(policy)
		for _, addr := range []string{"[fd00:ec2::254]:80", "100.100.100.200:80"} {
			conn, err := tr.DialContext(context.Background(), "tcp", addr)
			if err == nil {
				conn.Close()
				t.Fatalf("dial %s allowed under %v", addr, policy)
			}
			if !errors.Is(err, netguard.ErrDisallowedURL) {
				t.Errorf("dial %s under %v: err = %v, want ErrDisallowedURL", addr, policy, err)
			}
		}
	}
	// Neighbours of the metadata addresses keep their normal treatment.
	for _, raw := range []string{"http://[fd00:ec2::253]/", "http://100.100.100.201/"} {
		if _, err := netguard.ValidateURL(raw, netguard.AllowPrivate); err != nil {
			t.Errorf("ValidateURL(%q, AllowPrivate) err = %v, want nil", raw, err)
		}
	}
}

// A server allowed under AllowPrivate (loopback) that redirects to a
// link-local metadata address must not get the client there: the dial-time
// check refuses the redirect target.
func TestAllowPrivateClientRefusesRedirectToMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	client := netguard.NewClient(5*time.Second, time.Second, netguard.AllowPrivate)
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to the metadata address was followed")
	}
	if !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

func TestAllowPrivateTransportReachesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second, Transport: netguard.NewTransport(netguard.AllowPrivate)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/bot123:" + secret + "/sendMessage": "https://api.example.com/bot[REDACTED]/sendMessage",
		"https://h.example.com/p?key=" + secret + "&q=a":            "https://h.example.com/p?key=REDACTED&q=REDACTED",
		"https://u:" + secret + "@h.example.com/p":                  "https://h.example.com/p",
		"https://h.example.com/p":                                   "https://h.example.com/p",
	}
	for in, want := range cases {
		if got := netguard.RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := netguard.RedactURL("http://h.example.com/%zz?token=" + secret); strings.Contains(got, secret) {
		t.Errorf("RedactURL(unparseable) = %q leaks the secret", got)
	}
}

func TestRedactErrorURLError(t *testing.T) {
	inner := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	err := &url.Error{Op: "Get", URL: "https://h.example.com/p?token=" + secret, Err: inner}
	red := netguard.RedactError(err)
	if strings.Contains(red.Error(), secret) {
		t.Fatalf("redacted = %q", red)
	}
	if !strings.Contains(red.Error(), "h.example.com") || !strings.Contains(red.Error(), "connection refused") {
		t.Fatalf("redacted lost diagnostics: %q", red)
	}
	var opErr *net.OpError
	if !errors.As(red, &opErr) {
		t.Fatal("redacted error no longer unwraps to the transport error")
	}
	var ne net.Error
	if !errors.As(red, &ne) {
		t.Fatal("redacted error is no longer a net.Error")
	}
	var ue *url.Error
	if errors.As(red, &ue) {
		t.Fatal("redacted error still exposes the *url.Error carrying the URL")
	}
}

func TestRedactErrorKeepsContextAndTimeouts(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://h.example.com/?k=" + secret, Err: context.DeadlineExceeded}
	red := netguard.RedactError(err)
	if !errors.Is(red, context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded lost")
	}
	var ne interface{ Timeout() bool }
	if !errors.As(red, &ne) || !ne.Timeout() {
		t.Fatal("Timeout() lost")
	}
}

func TestRedactErrorWrappedAndExplicitSecrets(t *testing.T) {
	err := fmt.Errorf("outer: %w", &url.Error{Op: "Post", URL: "https://api.example.com/bot1:" + secret + "/sendMessage", Err: errors.New("EOF")})
	red := netguard.RedactError(err)
	if strings.Contains(red.Error(), secret) || !strings.HasPrefix(red.Error(), "outer: ") {
		t.Fatalf("redacted = %q", red)
	}

	plain := errors.New("auth failed for Authorization: Bearer " + secret + " and key " + secret)
	red = netguard.RedactError(plain, secret)
	if strings.Contains(red.Error(), secret) {
		t.Fatalf("redacted = %q", red)
	}
	if !errors.Is(red, plain) {
		t.Fatal("non-url error chain lost")
	}

	bearer := errors.New("header Authorization: Bearer abc.def-ghi")
	if strings.Contains(netguard.RedactError(bearer).Error(), "abc.def-ghi") {
		t.Fatal("bearer credential not redacted")
	}

	clean := errors.New("plain failure")
	if netguard.RedactError(clean) != clean {
		t.Fatal("clean errors must be returned unchanged")
	}
	if netguard.RedactError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	if netguard.RedactError(clean, "", "  ") != clean {
		t.Fatal("blank secrets must be ignored")
	}
}

func TestRedactErrorParseFailure(t *testing.T) {
	_, err := http.NewRequest(http.MethodGet, "http://h.example.com/%zz?token="+secret, nil)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if strings.Contains(netguard.RedactError(err).Error(), secret) {
		t.Fatalf("redacted = %q", netguard.RedactError(err))
	}
}

func TestHostPolicyString(t *testing.T) {
	if netguard.BlockPrivate.String() != "block-private" || netguard.AllowPrivate.String() != "allow-private" {
		t.Fatal("unexpected HostPolicy strings")
	}
}

func TestPolicyFor(t *testing.T) {
	if netguard.PolicyFor(false) != netguard.BlockPrivate || netguard.PolicyFor(true) != netguard.AllowPrivate {
		t.Fatal("PolicyFor mismatch")
	}
}

func TestNewClient(t *testing.T) {
	c := netguard.NewClient(0, 7*time.Second, netguard.BlockPrivate)
	if c.Timeout != 7*time.Second || c.CheckRedirect == nil || c.Transport == nil {
		t.Fatalf("NewClient = %+v", c)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatal("BlockPrivate transport must not use an environment proxy")
	}
	c = netguard.NewClient(2*time.Second, 7*time.Second, netguard.AllowPrivate)
	if c.Timeout != 2*time.Second {
		t.Fatalf("timeout = %v", c.Timeout)
	}
}

func TestBodyTooLarge(t *testing.T) {
	err := netguard.BodyTooLarge(42)
	if !errors.Is(err, netguard.ErrBodyTooLarge) || err.Error() != "response exceeds 42 bytes" {
		t.Fatalf("err = %v", err)
	}
}
