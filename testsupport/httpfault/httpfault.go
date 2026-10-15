// Package httpfault provides misbehaving httptest servers for the network
// client hardening tests: a server that never answers, one that drops the
// connection, one that redirects forever and one that streams an oversized
// body. Every server is closed through t.Cleanup.
package httpfault

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Secret is the credential the redaction tests plant in URLs and configs.
const Secret = "SECRET-TOKEN-123"

// Hanging returns a server whose handler blocks until the client goes away
// or the test ends.
func Hanging(t testing.TB) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

// Closing returns a server that hijacks and closes every connection
// without writing a response.
func Closing(t testing.TB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// RedirectLoop returns a server that redirects every request to itself
// and a counter of the requests it received.
func RedirectLoop(t testing.TB) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// Oversized returns a server that answers 200 with a body of n bytes
// (spaces, which also keep JSON and XML parsers reading).
func Oversized(t testing.TB, n int) *httptest.Server {
	t.Helper()
	body := strings.Repeat(" ", n)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}
