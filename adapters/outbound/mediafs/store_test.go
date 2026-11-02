package mediafs_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/mediafs"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
)

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// dialTo sends every connection to addr whatever host the URL names, so tests
// can use allow-listed fake host names against an httptest server while the
// URL, allow-list and redirect checks stay in force.
func dialTo(addr string) http.RoundTripper {
	return &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

type fixture struct {
	dir   string
	store *mediafs.Store
	srv   *httptest.Server
}

func newFixture(t *testing.T, h http.HandlerFunc, mutate func(*mediafs.Config)) *fixture {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := filepath.Join(t.TempDir(), "media")
	cfg := mediafs.Config{
		Dir:          dir,
		AllowedHosts: []string{"pbs.example.test"},
		Transport:    dialTo(srv.Listener.Addr().String()),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := mediafs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{dir: dir, store: s, srv: srv}
}

func serve(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestFetch_StoresImageUnderContentHashName(t *testing.T) {
	data := jpegBytes(t)
	f := newFixture(t, serve(data, "image/jpeg"), nil)

	ref, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if ref.MIME != "image/jpeg" || !strings.HasSuffix(ref.Path, ".jpg") || strings.ContainsAny(ref.Path, `/\`) {
		t.Fatalf("ref = %+v, want a flat <hash>.jpg image/jpeg", ref)
	}
	got, err := os.ReadFile(filepath.Join(f.dir, ref.Path))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored file = %v, %v; want the downloaded bytes", len(got), err)
	}
	again, err := f.store.Fetch(context.Background(), "http://pbs.example.test/other-url.jpg")
	if err != nil || again != ref {
		t.Fatalf("second fetch = %+v, %v; want the same ref %+v", again, err, ref)
	}
	if n := len(files(t, f.dir)); n != 1 {
		t.Fatalf("%d files after duplicate fetch, want 1", n)
	}
	if st := f.store.Stats(); st.Downloads != 1 || st.Reused != 1 || st.BytesWritten != int64(len(data)) || st.Rejects != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestFetch_TrustsContentNotNameOrHeader(t *testing.T) {
	f := newFixture(t, serve(pngBytes(t), "image/jpeg"), nil)
	ref, err := f.store.Fetch(context.Background(), "http://pbs.example.test/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if ref.MIME != "image/png" || !strings.HasSuffix(ref.Path, ".png") {
		t.Fatalf("ref = %+v, want the sniffed image/png", ref)
	}
}

func TestFetch_RejectsNonImages(t *testing.T) {
	mp4 := append([]byte{0, 0, 0, 0x18}, []byte("ftypmp42\x00\x00\x00\x00mp42isom")...)
	cases := map[string][]byte{
		"html served as image": []byte("<!DOCTYPE html><html><body>not an image</body></html>"),
		"video":                mp4,
		"gif":                  []byte("GIF89a\x01\x00\x01\x00"),
		"empty":                nil,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, serve(body, "image/jpeg"), nil)
			_, err := f.store.Fetch(context.Background(), "http://pbs.example.test/x.jpg")
			if !errors.Is(err, mediafs.ErrNotImage) {
				t.Fatalf("err = %v, want ErrNotImage", err)
			}
			if n := len(files(t, f.dir)); n != 0 {
				t.Fatalf("rejected download left %d files", n)
			}
			if f.store.Stats().Rejects != 1 {
				t.Fatalf("rejects = %d, want 1", f.store.Stats().Rejects)
			}
		})
	}
}

func TestFetch_RejectsOversizeBody(t *testing.T) {
	big := append(jpegBytes(t), bytes.Repeat([]byte{0}, 4096)...)
	for name, h := range map[string]http.HandlerFunc{
		"declared length": serve(big, "image/jpeg"),
		"chunked": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush() // no Content-Length
			}
			_, _ = w.Write(big)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, h, func(c *mediafs.Config) { c.MaxBytes = 1024 })
			_, err := f.store.Fetch(context.Background(), "http://pbs.example.test/big.jpg")
			if !errors.Is(err, netguard.ErrBodyTooLarge) {
				t.Fatalf("err = %v, want ErrBodyTooLarge", err)
			}
			if n := len(files(t, f.dir)); n != 0 {
				t.Fatalf("oversize download left %d files", n)
			}
		})
	}
}

func TestFetch_RejectsDisallowedTargets(t *testing.T) {
	f := newFixture(t, serve(jpegBytes(t), "image/jpeg"), nil)
	for name, tc := range map[string]struct {
		url  string
		want error
	}{
		"host not in allow-list": {"http://evil.example.test/a.jpg", mediafs.ErrHostNotAllowed},
		"loopback literal":       {"http://127.0.0.1/a.jpg", netguard.ErrDisallowedURL},
		"private literal":        {"http://10.0.0.5/a.jpg", netguard.ErrDisallowedURL},
		"metadata endpoint":      {"http://169.254.169.254/latest", netguard.ErrDisallowedURL},
		"file scheme":            {"file:///etc/passwd", netguard.ErrDisallowedURL},
		"ftp scheme":             {"ftp://pbs.example.test/a.jpg", netguard.ErrDisallowedURL},
		"credentials":            {"http://user:pw@pbs.example.test/a.jpg", netguard.ErrDisallowedURL},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.store.Fetch(context.Background(), tc.url); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFetch_BlocksPrivateAddressEvenWhenHostIsAllowListed(t *testing.T) {
	// A host operator mistake: allow-listing a loopback address still fails
	// under the default policy.
	s, err := mediafs.New(mediafs.Config{Dir: t.TempDir(), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch(context.Background(), "http://127.0.0.1:8080/a.jpg"); !errors.Is(err, netguard.ErrDisallowedURL) {
		t.Fatalf("err = %v, want ErrDisallowedURL", err)
	}
}

func TestFetch_RedirectTargetsAreChecked(t *testing.T) {
	for name, location := range map[string]string{
		"private ip":       "http://10.0.0.5/secret.jpg",
		"metadata":         "http://169.254.169.254/latest/meta-data",
		"other host":       "http://evil.example.test/a.jpg",
		"loopback literal": "http://127.0.0.1:9/a.jpg",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, location, http.StatusFound)
			}, nil)
			_, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg")
			if err == nil {
				t.Fatal("redirect accepted")
			}
			if !errors.Is(err, netguard.ErrDisallowedURL) && !errors.Is(err, mediafs.ErrHostNotAllowed) {
				t.Fatalf("err = %v, want a policy rejection", err)
			}
			if n := len(files(t, f.dir)); n != 0 {
				t.Fatalf("left %d files", n)
			}
		})
	}
}

func TestFetch_FollowsRedirectBetweenAllowedHosts(t *testing.T) {
	data := jpegBytes(t)
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "pbs.example.test" {
			http.Redirect(w, r, "http://cdn.example.test/final.jpg", http.StatusFound)
			return
		}
		serve(data, "image/jpeg")(w, r)
	}, func(c *mediafs.Config) { c.AllowedHosts = []string{"pbs.example.test", "CDN.example.test."} })
	if _, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg"); err != nil {
		t.Fatal(err)
	}
}

func TestFetch_RejectsErrorStatusAndSlowServers(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) }, nil)
	if _, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg"); err == nil {
		t.Fatal("404 accepted")
	}

	slow := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}, func(c *mediafs.Config) { c.Timeout = 50 * time.Millisecond })
	start := time.Now()
	if _, err := slow.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg"); err == nil {
		t.Fatal("timed-out download accepted")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestFetch_ErrorsDoNotLeakQueryValues(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusForbidden) }, nil)
	_, err := f.store.Fetch(context.Background(), "http://evil.example.test/a.jpg?sig=SECRETVALUE")
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Fatalf("err = %v", err)
	}
}

func TestNew_Validates(t *testing.T) {
	if _, err := mediafs.New(mediafs.Config{AllowedHosts: []string{"a.test"}}); err == nil {
		t.Error("missing dir accepted")
	}
	if _, err := mediafs.New(mediafs.Config{Dir: t.TempDir()}); err == nil {
		t.Error("empty allow-list accepted")
	}
	if _, err := mediafs.New(mediafs.Config{Dir: t.TempDir(), AllowedHosts: []string{" "}}); err == nil {
		t.Error("blank allow-list accepted")
	}
}

func TestDeleteOlderThan_DeletesOldFilesKeepsReferencedAndFresh(t *testing.T) {
	f := newFixture(t, serve(nil, ""), nil)
	if err := os.MkdirAll(f.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	write := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(f.dir, name)
		if err := os.WriteFile(p, []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	write("old.jpg", 8*day)
	write("old-referenced.jpg", 9*day)
	write("fresh.jpg", day)
	write(".tmp-abcdef", 8*day)
	write(".tmp-referenced-name", 8*day)
	if err := os.Mkdir(filepath.Join(f.dir, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}

	res, err := f.store.DeleteOlderThan(context.Background(), now.Add(-7*day), map[string]struct{}{
		"old-referenced.jpg": {}, ".tmp-referenced-name": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 3 || res.Protected != 1 || res.BytesDeleted != 15 {
		t.Fatalf("result = %+v, want deleted=3 protected=1 bytes=15", res)
	}
	got := strings.Join(files(t, f.dir), ",")
	if got != "fresh.jpg,old-referenced.jpg,subdir" {
		t.Fatalf("left %s", got)
	}
	if f.store.Stats().Deleted != 3 {
		t.Fatalf("stats = %+v", f.store.Stats())
	}

	// Once the referencing row is gone, the file goes too; a second run over
	// the same directory is harmless.
	res, err = f.store.DeleteOlderThan(context.Background(), now.Add(-7*day), nil)
	if err != nil || res.Deleted != 1 {
		t.Fatalf("second sweep = %+v, %v; want the formerly referenced file deleted", res, err)
	}
}

func TestDeleteOlderThan_MissingDirIsNotAnError(t *testing.T) {
	s, err := mediafs.New(mediafs.Config{Dir: filepath.Join(t.TempDir(), "never-created"), AllowedHosts: []string{"a.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.DeleteOlderThan(context.Background(), time.Now(), nil); err != nil || res.Deleted != 0 {
		t.Fatalf("= %+v, %v", res, err)
	}
}

func TestFetch_RefetchRenewsAgeSoTheFileSurvivesNextSweep(t *testing.T) {
	data := jpegBytes(t)
	f := newFixture(t, serve(data, "image/jpeg"), nil)
	ref, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.dir, ref.Path)
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Fetch(context.Background(), "http://pbs.example.test/a.jpg"); err != nil {
		t.Fatal(err)
	}
	res, err := f.store.DeleteOlderThan(context.Background(), time.Now().Add(-7*24*time.Hour), nil)
	if err != nil || res.Deleted != 0 {
		t.Fatalf("sweep after refetch = %+v, %v; want nothing deleted", res, err)
	}
}
