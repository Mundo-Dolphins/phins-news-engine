package bluesky_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	testHandle   = "isotopes.test"
	testPassword = "app-pass-word-secret"
	testDID      = "did:plc:isotopes"
)

var baseTime = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func jwt(exp time.Time) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"none"}`) + "." + enc(fmt.Sprintf(`{"exp":%d}`, exp.Unix())) + "." + enc("sig")
}

// fakePDS is a minimal Bluesky PDS recording every call.
type fakePDS struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	calls   []string
	records []map[string]any
	blobs   [][]byte
	tokens  []string

	accessLife time.Duration // lifetime of issued access tokens
	issued     int
	nowUnix    atomic.Int64 // the publisher's clock, shared with the server
	lastBody   []byte
	postedBody []byte // body of the latest createRecord call

	// hooks; nil means the happy path.
	createSession func(w http.ResponseWriter, r *http.Request) bool
	refresh       func(w http.ResponseWriter, r *http.Request) bool
	uploadBlob    func(w http.ResponseWriter, r *http.Request) bool
	createRecord  func(w http.ResponseWriter, r *http.Request) bool
	feed          func(w http.ResponseWriter, r *http.Request)
}

func newFakePDS(t *testing.T) *fakePDS {
	f := &fakePDS{t: t, accessLife: time.Hour}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePDS) count(endpoint string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == endpoint {
			n++
		}
	}
	return n
}

func (f *fakePDS) handle(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimPrefix(r.URL.Path, "/xrpc/")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, endpoint)
	f.tokens = append(f.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	f.lastBody = body
	if endpoint == "com.atproto.repo.createRecord" {
		f.postedBody = body
	}
	f.mu.Unlock()

	switch endpoint {
	case "com.atproto.server.createSession":
		if f.createSession != nil && f.createSession(w, r) {
			return
		}
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		if in["identifier"] != testHandle || in["password"] != testPassword {
			writeJSON(w, 401, `{"error":"AuthenticationRequired","message":"Invalid identifier or password"}`)
			return
		}
		f.issueSession(w)
	case "com.atproto.server.refreshSession":
		if f.refresh != nil && f.refresh(w, r) {
			return
		}
		f.issueSession(w)
	case "com.atproto.repo.uploadBlob":
		if f.uploadBlob != nil && f.uploadBlob(w, r) {
			return
		}
		f.mu.Lock()
		f.blobs = append(f.blobs, body)
		n := len(f.blobs)
		f.mu.Unlock()
		writeJSON(w, 200, fmt.Sprintf(`{"blob":{"$type":"blob","ref":{"$link":"bafkrei%d"},"mimeType":%q,"size":%d}}`, n, r.Header.Get("Content-Type"), len(body)))
	case "com.atproto.repo.createRecord":
		if f.createRecord != nil && f.createRecord(w, r) {
			return
		}
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		f.mu.Lock()
		f.records = append(f.records, in)
		n := len(f.records)
		f.mu.Unlock()
		writeJSON(w, 200, fmt.Sprintf(`{"uri":"at://%s/app.bsky.feed.post/3k%d","cid":"bafyrei%d"}`, testDID, n, n))
	case "app.bsky.feed.getAuthorFeed":
		if f.feed == nil {
			writeJSON(w, 200, `{"feed":[]}`)
			return
		}
		f.feed(w, r)
	default:
		f.t.Errorf("unexpected endpoint %s", endpoint)
		w.WriteHeader(404)
	}
}

func (f *fakePDS) issueSession(w http.ResponseWriter) {
	f.mu.Lock()
	f.issued++
	n := f.issued
	life := f.accessLife
	f.mu.Unlock()
	writeJSON(w, 200, fmt.Sprintf(`{"accessJwt":%q,"refreshJwt":"refresh-%d","did":%q,"handle":%q}`,
		jwt(time.Unix(f.nowUnix.Load(), 0).Add(life)), n, testDID, testHandle))
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// publisher builds a Publisher on the fake's clock and returns a setter for it.
func (f *fakePDS) publisher(t *testing.T, mediaRoot string) (*bluesky.Publisher, func(time.Time)) {
	f.nowUnix.Store(baseTime.Unix())
	p, err := bluesky.NewPublisher(bluesky.Config{
		Handle: testHandle, AppPassword: testPassword,
		Host: f.server.URL, AppViewHost: f.server.URL, MediaRoot: mediaRoot,
		Timeout: 2 * time.Second, Now: func() time.Time { return time.Unix(f.nowUnix.Load(), 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	return p, func(at time.Time) { f.nowUnix.Store(at.Unix()) }
}

var testArticle = article.Article{ID: "a1", Title: "Isotopes sign a long snapper", CanonicalURL: "https://example.com/story"}

func textPayload(text string) publishing.MessagePayload { return publishing.MessagePayload{Text: text} }

func TestPublish_LoginThenCreateRecord(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")

	uri, err := p.PublishPayload(context.Background(), testArticle, textPayload("Hola Springfield"))
	if err != nil {
		t.Fatalf("PublishPayload() error = %v", err)
	}
	if uri != "at://"+testDID+"/app.bsky.feed.post/3k1" {
		t.Errorf("uri = %q", uri)
	}
	if got := pds.calls; len(got) != 2 || got[0] != "com.atproto.server.createSession" || got[1] != "com.atproto.repo.createRecord" {
		t.Fatalf("calls = %v", got)
	}
	if pds.tokens[1] == "" {
		t.Error("createRecord sent without a bearer token")
	}
	in := pds.records[0]
	if in["repo"] != testDID || in["collection"] != "app.bsky.feed.post" {
		t.Errorf("repo/collection = %v/%v", in["repo"], in["collection"])
	}
	rec := obj(t, in["record"])
	if rec["$type"] != "app.bsky.feed.post" || rec["text"] != "Hola Springfield" {
		t.Errorf("record = %v", rec)
	}
	if rec["createdAt"] != "2026-08-25T12:00:00.000Z" {
		t.Errorf("createdAt = %v", rec["createdAt"])
	}
	if langs, _ := rec["langs"].([]any); len(langs) != 1 || langs[0] != "es" {
		t.Errorf("langs = %v", rec["langs"])
	}
	if _, has := rec["embed"]; has {
		t.Error("text-only post carries an embed")
	}
}

func TestPublish_EmptyPayloadUsesArticle(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	if _, err := p.Publish(context.Background(), testArticle); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	rec := obj(t, pds.records[0]["record"])
	if rec["text"] != "Isotopes sign a long snapper\n\nhttps://example.com/story" {
		t.Errorf("text = %q", rec["text"])
	}
	if facets, _ := rec["facets"].([]any); len(facets) != 1 {
		t.Errorf("facets = %v, want the link facet", rec["facets"])
	}
}

func TestPublish_ReusesSessionAndRefreshesBeforeExpiry(t *testing.T) {
	pds := newFakePDS(t)
	pds.accessLife = 10 * time.Minute
	p, setNow := pds.publisher(t, "")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := p.PublishPayload(ctx, testArticle, textPayload("uno")); err != nil {
			t.Fatal(err)
		}
	}
	if pds.count("com.atproto.server.createSession") != 1 || pds.count("com.atproto.server.refreshSession") != 0 {
		t.Fatalf("calls = %v, want one login and no refresh while the token is fresh", pds.calls)
	}

	setNow(baseTime.Add(9 * time.Minute)) // inside the refresh margin
	if _, err := p.PublishPayload(ctx, testArticle, textPayload("dos")); err != nil {
		t.Fatal(err)
	}
	if pds.count("com.atproto.server.refreshSession") != 1 || pds.count("com.atproto.server.createSession") != 1 {
		t.Fatalf("calls = %v, want a refresh and no second login", pds.calls)
	}
	// The refresh call authenticates with the refresh token, not the access token.
	for i, c := range pds.calls {
		if c == "com.atproto.server.refreshSession" && pds.tokens[i] != "refresh-1" {
			t.Errorf("refresh used token %q, want the refresh JWT", pds.tokens[i])
		}
	}
}

func TestPublish_RefreshFailureFallsBackToLogin(t *testing.T) {
	pds := newFakePDS(t)
	pds.accessLife = 10 * time.Minute
	pds.refresh = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 400, `{"error":"ExpiredToken","message":"Token has expired"}`)
		return true
	}
	p, setNow := pds.publisher(t, "")
	ctx := context.Background()
	if _, err := p.PublishPayload(ctx, testArticle, textPayload("uno")); err != nil {
		t.Fatal(err)
	}
	setNow(baseTime.Add(9 * time.Minute))
	if _, err := p.PublishPayload(ctx, testArticle, textPayload("dos")); err != nil {
		t.Fatalf("PublishPayload() error = %v, want a re-login after the failed refresh", err)
	}
	if pds.count("com.atproto.server.createSession") != 2 {
		t.Fatalf("calls = %v, want a second login", pds.calls)
	}
}

func TestPublish_ExpiredTokenRejectedByServerIsRenewedOnce(t *testing.T) {
	pds := newFakePDS(t)
	rejected := false
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		if !rejected {
			rejected = true
			writeJSON(w, 400, `{"error":"ExpiredToken","message":"Token has expired"}`)
			return true
		}
		return false
	}
	p, _ := pds.publisher(t, "")
	if _, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola")); err != nil {
		t.Fatalf("PublishPayload() error = %v", err)
	}
	if pds.count("com.atproto.server.refreshSession") != 1 || pds.count("com.atproto.repo.createRecord") != 2 {
		t.Fatalf("calls = %v", pds.calls)
	}
}

func TestPublish_ConcurrentPublishesShareOneLogin(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := pds.count("com.atproto.server.createSession"); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
	if n := pds.count("com.atproto.repo.createRecord"); n != 8 {
		t.Fatalf("createRecord calls = %d, want 8", n)
	}
}

func TestPublish_LoginRejectedIsTemporaryAndLeaksNoSecrets(t *testing.T) {
	pds := newFakePDS(t)
	p, err := bluesky.NewPublisher(bluesky.Config{
		Handle: testHandle, AppPassword: "wrong-password-value", Host: pds.server.URL, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	if err == nil {
		t.Fatal("want an error for rejected credentials")
	}
	var perm *appout.PermanentError
	var amb *appout.AmbiguousError
	if errors.As(err, &perm) || errors.As(err, &amb) {
		t.Fatalf("error %v is permanent/ambiguous; bad credentials must stay retryable", err)
	}
	if strings.Contains(err.Error(), "wrong-password-value") {
		t.Errorf("error leaks the app password: %v", err)
	}
	if pds.count("com.atproto.repo.createRecord") != 0 {
		t.Error("createRecord was called without a session")
	}
}

func TestPublish_RateLimited(t *testing.T) {
	pds := newFakePDS(t)
	reset := baseTime.Add(90 * time.Second)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		writeJSON(w, 429, `{"error":"RateLimitExceeded","message":"Rate Limit Exceeded"}`)
		return true
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	var rl *appout.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("error = %v, want RateLimitedError", err)
	}
	if rl.RetryAfter != 90*time.Second {
		t.Errorf("RetryAfter = %v, want 90s from RateLimit-Reset", rl.RetryAfter)
	}
	var amb *appout.AmbiguousError
	if errors.As(err, &amb) {
		t.Error("a parsed 429 is a confirmed failure, not ambiguous")
	}
}

func TestPublish_RateLimitedWithoutHeaderUsesDefault(t *testing.T) {
	pds := newFakePDS(t)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 429, `{"error":"RateLimitExceeded"}`)
		return true
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	var rl *appout.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("error = %v, want RateLimitedError with a positive RetryAfter", err)
	}
}

func TestPublish_ValidationErrorIsPermanent(t *testing.T) {
	pds := newFakePDS(t)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 400, `{"error":"InvalidRequest","message":"Record/text must not be longer than 3000 bytes"}`)
		return true
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	var perm *appout.PermanentError
	if !errors.As(err, &perm) {
		t.Fatalf("error = %v, want PermanentError", err)
	}
	if !strings.Contains(err.Error(), "InvalidRequest") {
		t.Errorf("error %q lacks the API error code", err)
	}
}

func TestPublish_ServerErrorWithAPIBodyIsTemporary(t *testing.T) {
	pds := newFakePDS(t)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 500, `{"error":"InternalServerError","message":"boom"}`)
		return true
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	var perm *appout.PermanentError
	var amb *appout.AmbiguousError
	if err == nil || errors.As(err, &perm) || errors.As(err, &amb) {
		t.Fatalf("error = %v, want a plain temporary error", err)
	}
}

func TestPublish_AmbiguousOutcomes(t *testing.T) {
	tests := []struct {
		name string
		hook func(w http.ResponseWriter, r *http.Request) bool
	}{
		{"timeout", func(w http.ResponseWriter, r *http.Request) bool {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			return true
		}},
		{"dropped connection", func(w http.ResponseWriter, r *http.Request) bool {
			hijackClose(w)
			return true
		}},
		{"unreadable success body", func(w http.ResponseWriter, r *http.Request) bool {
			writeJSON(w, 200, `<html>not json`)
			return true
		}},
		{"success without uri", func(w http.ResponseWriter, r *http.Request) bool {
			writeJSON(w, 200, `{}`)
			return true
		}},
		{"gateway error without api body", func(w http.ResponseWriter, r *http.Request) bool {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := newFakePDS(t)
			pds.createRecord = tt.hook
			p, err := bluesky.NewPublisher(bluesky.Config{
				Handle: testHandle, AppPassword: testPassword, Host: pds.server.URL,
				Timeout: 200 * time.Millisecond, Now: func() time.Time { return baseTime },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
			var amb *appout.AmbiguousError
			if !errors.As(err, &amb) {
				t.Fatalf("error = %v, want AmbiguousError", err)
			}
		})
	}
}

func TestPublish_FailureBeforeCreateRecordIsNeverAmbiguous(t *testing.T) {
	pds := newFakePDS(t)
	pds.uploadBlob = func(w http.ResponseWriter, r *http.Request) bool {
		hijackClose(w)
		return true
	}
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "a.png"), 40, 30, false)
	p, _ := pds.publisher(t, root)
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Text: "x", Images: []publishing.ImageRef{{Path: "a.png", MIME: "image/png", Alt: "a"}},
	})
	var amb *appout.AmbiguousError
	if err == nil || errors.As(err, &amb) {
		t.Fatalf("error = %v, want a temporary error: nothing was posted", err)
	}
	if pds.count("com.atproto.repo.createRecord") != 0 {
		t.Error("createRecord was called after a failed upload")
	}
}

func TestPublish_GraphemeLimit(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"300 ascii", strings.Repeat("a", 300), false},
		{"301 ascii", strings.Repeat("a", 301), true},
		{"300 emoji", strings.Repeat("🏈", 300), true}, // 1200 bytes is fine, but see below
		{"300 accented combining", strings.Repeat("é", 300), false},
		{"301 accented combining", strings.Repeat("é", 301), true},
		{"300 precomposed", strings.Repeat("é", 300), false},
		{"300 family emoji", strings.Repeat("👨‍👩‍👧‍👦", 100), false},
	}
	// 300 football emoji are 1200 bytes: valid. Correct the table entry.
	tests[2].wantErr = false
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := newFakePDS(t)
			p, _ := pds.publisher(t, "")
			_, err := p.PublishPayload(context.Background(), testArticle, textPayload(tt.text))
			var perm *appout.PermanentError
			if tt.wantErr != errors.As(err, &perm) {
				t.Fatalf("error = %v, wantPermanent %v", err, tt.wantErr)
			}
			if tt.wantErr && len(pds.calls) != 0 {
				t.Errorf("calls = %v, want no network call for invalid text", pds.calls)
			}
			if !tt.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublish_NothingToPostIsPermanent(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), article.Article{ID: "a2"}, publishing.MessagePayload{})
	var perm *appout.PermanentError
	if !errors.As(err, &perm) || len(pds.calls) != 0 {
		t.Fatalf("error = %v, calls = %v; want PermanentError and no network call", err, pds.calls)
	}
}

func TestPublish_ByteLimitIsPermanent(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	// 200 family emoji: 200 graphemes but 5,000 bytes.
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload(strings.Repeat("👨‍👩‍👧‍👦", 200)))
	var perm *appout.PermanentError
	if !errors.As(err, &perm) {
		t.Fatalf("error = %v, want PermanentError for >3000 bytes", err)
	}
}

func TestPublish_LinkAppendedAndFaceted(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Text: "Última hora #Isotopes", Link: "https://example.com/informe",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := obj(t, pds.records[0]["record"])
	text := str(t, rec["text"])
	if text != "Última hora #Isotopes\n\nhttps://example.com/informe" {
		t.Fatalf("text = %q", text)
	}
	facets := arr(t, rec["facets"])
	if len(facets) != 2 {
		t.Fatalf("facets = %v", facets)
	}
	// Offsets are UTF-8 bytes: "Ú" is two bytes, so the tag starts at byte 14.
	tag := obj(t, facets[0])
	idx := obj(t, tag["index"])
	start, end := num(t, idx["byteStart"]), num(t, idx["byteEnd"])
	if text[start:end] != "#Isotopes" {
		t.Errorf("tag facet covers %q", text[start:end])
	}
	if f := obj(t, arr(t, tag["features"])[0]); f["$type"] != "app.bsky.richtext.facet#tag" || f["tag"] != "Isotopes" {
		t.Errorf("tag feature = %v", f)
	}
	link := obj(t, facets[1])
	idx = obj(t, link["index"])
	start, end = num(t, idx["byteStart"]), num(t, idx["byteEnd"])
	if text[start:end] != "https://example.com/informe" {
		t.Errorf("link facet covers %q", text[start:end])
	}
	if f := obj(t, arr(t, link["features"])[0]); f["$type"] != "app.bsky.richtext.facet#link" || f["uri"] != "https://example.com/informe" {
		t.Errorf("link feature = %v", f)
	}
}

func TestPublish_LinkAlreadyInTextIsNotDuplicated(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Text: "Mira https://example.com/informe.", Link: "https://example.com/informe",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := obj(t, pds.records[0]["record"])
	if rec["text"] != "Mira https://example.com/informe." {
		t.Errorf("text = %q", rec["text"])
	}
	facets := arr(t, rec["facets"])
	idx := obj(t, obj(t, facets[0])["index"])
	if got := str(t, rec["text"])[num(t, idx["byteStart"]):num(t, idx["byteEnd"])]; got != "https://example.com/informe" {
		t.Errorf("link facet covers %q, want the URL without the trailing dot", got)
	}
}

func writePNG(t *testing.T, path string, w, h int, noisy bool) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x), uint8(y), 128, 255}
			if noisy {
				c = color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255}
			}
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestPublish_ImagesAreUploadedAndEmbeddedWithAlt(t *testing.T) {
	pds := newFakePDS(t)
	root := t.TempDir()
	var refs []publishing.ImageRef
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("img%d.png", i)
		writePNG(t, filepath.Join(root, name), 40+i, 30, false)
		refs = append(refs, publishing.ImageRef{Path: name, MIME: "image/png", Alt: fmt.Sprintf("Alt número %d", i)})
	}
	p, _ := pds.publisher(t, root)
	if _, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{Text: "Informe", Images: refs}); err != nil {
		t.Fatalf("PublishPayload() error = %v", err)
	}
	if pds.count("com.atproto.repo.uploadBlob") != 4 {
		t.Fatalf("calls = %v, want 4 uploads", pds.calls)
	}
	embed := obj(t, obj(t, pds.records[0]["record"])["embed"])
	if embed["$type"] != "app.bsky.embed.images" {
		t.Errorf("embed type = %v", embed["$type"])
	}
	images := arr(t, embed["images"])
	if len(images) != 4 {
		t.Fatalf("embedded images = %d, want 4", len(images))
	}
	for i, raw := range images {
		im := obj(t, raw)
		if im["alt"] != fmt.Sprintf("Alt número %d", i) {
			t.Errorf("image %d alt = %v", i, im["alt"])
		}
		blob := obj(t, im["image"])
		if blob["$type"] != "blob" || blob["mimeType"] != "image/png" {
			t.Errorf("image %d blob = %v", i, blob)
		}
		ar := obj(t, im["aspectRatio"])
		if num(t, ar["width"]) != 40+i || num(t, ar["height"]) != 30 {
			t.Errorf("image %d aspectRatio = %v", i, ar)
		}
	}
}

func TestPublish_ImageOnlyPostIsAllowed(t *testing.T) {
	pds := newFakePDS(t)
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "a.png"), 20, 20, false)
	p, _ := pds.publisher(t, root)
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Images: []publishing.ImageRef{{Path: "a.png", MIME: "image/png", Alt: "solo imagen"}},
	})
	if err != nil {
		t.Fatalf("PublishPayload() error = %v", err)
	}
}

func TestPublish_ImageProblemsArePermanentAndSendNothing(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "ok.png"), 20, 20, false)
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("<html>no image</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	writePNG(t, outside, 20, 20, false)
	if err := os.Symlink(outside, filepath.Join(root, "link.png")); err != nil {
		t.Fatal(err)
	}
	// Large webp-labelled file: cannot be re-encoded.
	big := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 1_100_000)...)
	if err := os.WriteFile(filepath.Join(root, "big.webp"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	five := make([]publishing.ImageRef, 5)
	for i := range five {
		five[i] = publishing.ImageRef{Path: "ok.png", MIME: "image/png"}
	}
	tests := []struct {
		name   string
		images []publishing.ImageRef
	}{
		{"five images", five},
		{"unsupported type", []publishing.ImageRef{{Path: "ok.png", MIME: "image/gif"}}},
		{"content is not an image", []publishing.ImageRef{{Path: "fake.png", MIME: "image/png"}}},
		{"declared type mismatches content", []publishing.ImageRef{{Path: "ok.png", MIME: "image/jpeg"}}},
		{"missing file", []publishing.ImageRef{{Path: "nope.png", MIME: "image/png"}}},
		{"path escape", []publishing.ImageRef{{Path: "../x.png", MIME: "image/png"}}},
		{"symlink out of the store", []publishing.ImageRef{{Path: "link.png", MIME: "image/png"}}},
		{"oversized webp", []publishing.ImageRef{{Path: "big.webp", MIME: "image/webp"}}},
		{"alt too long", []publishing.ImageRef{{Path: "ok.png", MIME: "image/png", Alt: strings.Repeat("a", 2001)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := newFakePDS(t)
			p, _ := pds.publisher(t, root)
			_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{Text: "x", Images: tt.images})
			var perm *appout.PermanentError
			if !errors.As(err, &perm) {
				t.Fatalf("error = %v, want PermanentError", err)
			}
			if len(pds.calls) != 0 {
				t.Errorf("calls = %v, want no network call", pds.calls)
			}
		})
	}
}

func TestPublish_NoMediaRootRejectsImages(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Text: "x", Images: []publishing.ImageRef{{Path: "a.png", MIME: "image/png"}},
	})
	var perm *appout.PermanentError
	if !errors.As(err, &perm) {
		t.Fatalf("error = %v, want PermanentError", err)
	}
}

func TestPublish_OversizedPNGIsReencodedUnderTheLimit(t *testing.T) {
	pds := newFakePDS(t)
	root := t.TempDir()
	path := filepath.Join(root, "big.png")
	writePNG(t, path, 900, 700, true) // incompressible noise: ~1.9 MB
	if fi, _ := os.Stat(path); fi.Size() <= 1_000_000 {
		t.Fatalf("fixture is only %d bytes", fi.Size())
	}
	p, _ := pds.publisher(t, root)
	_, err := p.PublishPayload(context.Background(), testArticle, publishing.MessagePayload{
		Text: "Foto", Images: []publishing.ImageRef{{Path: "big.png", MIME: "image/png", Alt: "ruido"}},
	})
	if err != nil {
		t.Fatalf("PublishPayload() error = %v", err)
	}
	if len(pds.blobs) != 1 || len(pds.blobs[0]) > 1_000_000 {
		t.Fatalf("uploaded %d blobs, first %d bytes; want one under 1,000,000", len(pds.blobs), len(pds.blobs[0]))
	}
	img := obj(t, arr(t, obj(t, obj(t, pds.records[0]["record"])["embed"])["images"])[0])
	if mt := obj(t, img["image"])["mimeType"]; mt != "image/jpeg" {
		t.Errorf("re-encoded blob mimeType = %v, want image/jpeg", mt)
	}
	if http.DetectContentType(pds.blobs[0]) != "image/jpeg" {
		t.Error("uploaded bytes are not a JPEG")
	}
}

func TestPublish_ErrorsNeverContainCredentials(t *testing.T) {
	pds := newFakePDS(t)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 400, `{"error":"InvalidRequest","message":"bad"}`)
		return true
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("hola"))
	for _, secret := range []string{testPassword, "refresh-1", "eyJ", "Bearer"} {
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestNewPublisher_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  bluesky.Config
	}{
		{"no handle", bluesky.Config{AppPassword: "x"}},
		{"no password", bluesky.Config{Handle: "a.test"}},
		{"link-local host", bluesky.Config{Handle: "a.test", AppPassword: "x", Host: "http://169.254.169.254"}},
		{"non-http host", bluesky.Config{Handle: "a.test", AppPassword: "x", Host: "ftp://pds.test"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := bluesky.NewPublisher(tt.cfg); err == nil {
				t.Fatal("NewPublisher() error = nil")
			}
		})
	}
}

// --- reconciliation ---

func feedJSON(posts ...string) string { return `{"feed":[` + strings.Join(posts, ",") + `]}` }

func feedPost(uri, handle, text string, createdAt time.Time) string {
	return fmt.Sprintf(`{"post":{"uri":%q,"author":{"handle":%q},"record":{"text":%q,"createdAt":%q}}}`,
		uri, handle, text, createdAt.UTC().Format("2006-01-02T15:04:05.000Z"))
}

func TestFindSent(t *testing.T) {
	sentAt := baseTime
	skew := 2 * time.Minute
	want := "Informe de lesionados"
	tests := []struct {
		name        string
		feed        string
		wantID      string
		wantMatched bool
		wantCovered bool
	}{
		{"match", feedJSON(
			feedPost("at://d/p/2", testHandle, want, sentAt.Add(-10*time.Second)),
			feedPost("at://d/p/1", testHandle, "otro", sentAt.Add(-time.Hour))), "at://d/p/2", true, true},
		{"covered non-match", feedJSON(
			feedPost("at://d/p/2", testHandle, "otro", sentAt),
			feedPost("at://d/p/1", testHandle, want, sentAt.Add(-time.Hour))), "", false, true},
		{"same text but posted long before the send", feedJSON(
			feedPost("at://d/p/1", testHandle, want, sentAt.Add(-time.Hour))), "", false, true},
		{"feed does not reach back", feedJSON(
			feedPost("at://d/p/2", testHandle, "otro", sentAt.Add(time.Minute))), "", false, false},
		{"empty feed is inconclusive", feedJSON(), "", false, false},
		{"someone else's repost is ignored", feedJSON(
			feedPost("at://other/p/9", "other.test", want, sentAt),
			feedPost("at://d/p/1", testHandle, "otro", sentAt.Add(-time.Hour))), "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := newFakePDS(t)
			var query string
			pds.feed = func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				writeJSON(w, 200, tt.feed)
			}
			p, _ := pds.publisher(t, "")
			ref, matched, covered, err := p.FindSent(context.Background(), appout.SentLookup{
				Article: testArticle, Payload: textPayload(want), SentAt: sentAt, ClockSkew: skew,
			})
			if err != nil {
				t.Fatalf("FindSent() error = %v", err)
			}
			id := ref.URI
			if id != tt.wantID || matched != tt.wantMatched || covered != tt.wantCovered {
				t.Fatalf("FindSent() = (%q, %v, %v), want (%q, %v, %v)", id, matched, covered, tt.wantID, tt.wantMatched, tt.wantCovered)
			}
			if !strings.Contains(query, "actor="+testHandle) {
				t.Errorf("query = %q, want the account's own feed", query)
			}
		})
	}
}

func TestFindSent_MatchesArticleDerivedText(t *testing.T) {
	pds := newFakePDS(t)
	text := "Isotopes sign a long snapper\n\nhttps://example.com/story"
	pds.feed = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, feedJSON(feedPost("at://d/p/1", testHandle, text, baseTime)))
	}
	p, _ := pds.publisher(t, "")
	ref, matched, _, err := p.FindSent(context.Background(), appout.SentLookup{
		Article: testArticle, SentAt: baseTime, ClockSkew: time.Minute,
	})
	if id := ref.URI; err != nil || !matched || id != "at://d/p/1" {
		t.Fatalf("FindSent() = (%q, %v, %v)", id, matched, err)
	}
}

func TestFindSent_LookupFailureIsAnError(t *testing.T) {
	pds := newFakePDS(t)
	pds.feed = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, `{}`) }
	p, _ := pds.publisher(t, "")
	if _, _, _, err := p.FindSent(context.Background(), appout.SentLookup{Article: testArticle, Payload: textPayload("x"), SentAt: baseTime}); err == nil {
		t.Fatal("FindSent() error = nil, want the lookup failure")
	}
}

func TestAmbiguousPublishThenReconcileFindsThePost(t *testing.T) {
	// The post lands server-side but the reply is lost; reconciliation finds it.
	pds := newFakePDS(t)
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		hijackClose(w)
		return true
	}
	pds.feed = func(w http.ResponseWriter, r *http.Request) {
		pds.mu.Lock()
		raw := pds.postedBody
		pds.mu.Unlock()
		var in struct {
			Record struct {
				Text      string `json:"text"`
				CreatedAt string `json:"createdAt"`
			} `json:"record"`
		}
		_ = json.Unmarshal(raw, &in)
		writeJSON(w, 200, `{"feed":[{"post":{"uri":"at://d/p/77","author":{"handle":"`+testHandle+`"},"record":{"text":`+strconv.Quote(in.Record.Text)+`,"createdAt":"`+in.Record.CreatedAt+`"}}}]}`)
	}
	p, _ := pds.publisher(t, "")
	_, err := p.PublishPayload(context.Background(), testArticle, textPayload("Hola"))
	var amb *appout.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("error = %v, want AmbiguousError", err)
	}
	ref, matched, covered, err := p.FindSent(context.Background(), appout.SentLookup{
		Article: testArticle, Payload: textPayload("Hola"), SentAt: baseTime.Add(time.Second), ClockSkew: time.Minute,
	})
	if id := ref.URI; err != nil || !matched || !covered || id != "at://d/p/77" {
		t.Fatalf("FindSent() = (%q, %v, %v, %v)", id, matched, covered, err)
	}
}

func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want a JSON object", v)
	}
	return m
}

func arr(t *testing.T, v any) []any {
	t.Helper()
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("got %T, want a JSON array", v)
	}
	return a
}

// hijackClose drops the connection without replying.
func hijackClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	if conn, _, err := hj.Hijack(); err == nil {
		_ = conn.Close()
	}
}

func num(t *testing.T, v any) int {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("got %T, want a JSON number", v)
	}
	return int(f)
}

func str(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("got %T, want a JSON string", v)
	}
	return s
}

// --- threads ---

func threadPayload(parts ...string) publishing.MessagePayload {
	return publishing.MessagePayload{Text: parts[0], Continuations: parts[1:]}
}

// postedRef is the ref the fake PDS returns for its n-th created record.
func postedRef(n int) publishing.PostRef {
	return publishing.PostRef{URI: fmt.Sprintf("at://%s/app.bsky.feed.post/3k%d", testDID, n), CID: fmt.Sprintf("bafyrei%d", n)}
}

// replyOf returns the record's reply root and parent, or nil when it has none.
func replyOf(t *testing.T, in map[string]any) (root, parent *publishing.PostRef) {
	t.Helper()
	reply, ok := obj(t, in["record"])["reply"]
	if !ok {
		return nil, nil
	}
	ref := func(v any) *publishing.PostRef {
		m := obj(t, v)
		return &publishing.PostRef{URI: str(t, m["uri"]), CID: str(t, m["cid"])}
	}
	r := obj(t, reply)
	return ref(r["root"]), ref(r["parent"])
}

func TestPublishThread_PostsRepliesWithRootAndParent(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	var recorded []publishing.PostRef
	uri, err := p.PublishThread(context.Background(), testArticle, threadPayload("Uno.", "Dos.", "Tres."), nil,
		func(ref publishing.PostRef) error { recorded = append(recorded, ref); return nil })
	if err != nil {
		t.Fatalf("PublishThread() error = %v", err)
	}
	if uri != postedRef(1).URI {
		t.Errorf("uri = %q, want the first part's %q", uri, postedRef(1).URI)
	}
	if len(pds.records) != 3 {
		t.Fatalf("createRecord calls = %d, want 3", len(pds.records))
	}
	for i, want := range []string{"Uno.", "Dos.", "Tres."} {
		rec := obj(t, pds.records[i]["record"])
		if rec["text"] != want {
			t.Errorf("part %d text = %v, want %q", i, rec["text"], want)
		}
		if langs := arr(t, rec["langs"]); len(langs) != 1 || langs[0] != "es" {
			t.Errorf("part %d langs = %v", i, langs)
		}
	}
	if root, parent := replyOf(t, pds.records[0]); root != nil || parent != nil {
		t.Errorf("part 0 has a reply: root=%v parent=%v", root, parent)
	}
	if root, parent := replyOf(t, pds.records[1]); root == nil || *root != postedRef(1) || *parent != postedRef(1) {
		t.Errorf("part 1 reply root=%v parent=%v, want both %v", root, parent, postedRef(1))
	}
	if root, parent := replyOf(t, pds.records[2]); root == nil || *root != postedRef(1) || *parent != postedRef(2) {
		t.Errorf("part 2 reply root=%v parent=%v, want root %v parent %v", root, parent, postedRef(1), postedRef(2))
	}
	if want := []publishing.PostRef{postedRef(1), postedRef(2), postedRef(3)}; fmt.Sprint(recorded) != fmt.Sprint(want) {
		t.Errorf("recorded = %v, want %v", recorded, want)
	}
}

func TestPublishThread_LinkAndFacetsOnFirstPart(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	payload := threadPayload("Uno.", "Mira https://example.com/dos")
	payload.Link = "https://example.com/uno"
	if _, err := p.PublishThread(context.Background(), testArticle, payload, nil, func(publishing.PostRef) error { return nil }); err != nil {
		t.Fatalf("PublishThread() error = %v", err)
	}
	for i, want := range []string{"Uno.\n\nhttps://example.com/uno", "Mira https://example.com/dos"} {
		rec := obj(t, pds.records[i]["record"])
		if rec["text"] != want {
			t.Errorf("part %d text = %q, want %q", i, rec["text"], want)
		}
		if len(arr(t, rec["facets"])) != 1 {
			t.Errorf("part %d facets = %v, want the link facet", i, rec["facets"])
		}
	}
}

func TestPublishThread_SkipsDone(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	first := publishing.PostRef{URI: "at://" + testDID + "/app.bsky.feed.post/root", CID: "bafyroot"}
	var recorded []publishing.PostRef
	uri, err := p.PublishThread(context.Background(), testArticle, threadPayload("Uno.", "Dos.", "Tres."),
		[]publishing.PostRef{first}, func(ref publishing.PostRef) error { recorded = append(recorded, ref); return nil })
	if err != nil {
		t.Fatalf("PublishThread() error = %v", err)
	}
	if uri != first.URI {
		t.Errorf("uri = %q, want the done root %q", uri, first.URI)
	}
	if n := pds.count("com.atproto.repo.createRecord"); n != 2 {
		t.Fatalf("createRecord calls = %d, want 2", n)
	}
	if text := obj(t, pds.records[0]["record"])["text"]; text != "Dos." {
		t.Errorf("first posted text = %v, want the second part", text)
	}
	if root, parent := replyOf(t, pds.records[0]); root == nil || *root != first || *parent != first {
		t.Errorf("first posted reply root=%v parent=%v, want both %v", root, parent, first)
	}
	if root, parent := replyOf(t, pds.records[1]); root == nil || *root != first || *parent != postedRef(1) {
		t.Errorf("second posted reply root=%v parent=%v", root, parent)
	}
	if len(recorded) != 2 {
		t.Errorf("record calls = %d, want 2", len(recorded))
	}
}

func TestPublishThread_AllDoneReturnsRoot(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	done := []publishing.PostRef{{URI: "at://d/p/1", CID: "c1"}, {URI: "at://d/p/2", CID: "c2"}}
	calls := 0
	uri, err := p.PublishThread(context.Background(), testArticle, threadPayload("Uno.", "Dos."), done,
		func(publishing.PostRef) error { calls++; return nil })
	if err != nil || uri != "at://d/p/1" {
		t.Fatalf("PublishThread() = (%q, %v), want (\"at://d/p/1\", nil)", uri, err)
	}
	if calls != 0 || len(pds.calls) != 0 {
		t.Fatalf("record calls = %d, network calls = %v; want none", calls, pds.calls)
	}
}

func TestPublishThread_PartOverLimitIsPermanent(t *testing.T) {
	for name, payload := range map[string]publishing.MessagePayload{
		"graphemes": threadPayload("Uno.", "Dos.", strings.Repeat("a", 301)),
		"bytes":     threadPayload("Uno.", strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 200)),
	} {
		t.Run(name, func(t *testing.T) {
			pds := newFakePDS(t)
			p, _ := pds.publisher(t, "")
			calls := 0
			_, err := p.PublishThread(context.Background(), testArticle, payload, nil,
				func(publishing.PostRef) error { calls++; return nil })
			var perm *appout.PermanentError
			if !errors.As(err, &perm) {
				t.Fatalf("error = %v, want PermanentError", err)
			}
			if len(pds.calls) != 0 || calls != 0 {
				t.Fatalf("network calls = %v, record calls = %d; want none", pds.calls, calls)
			}
		})
	}
}

func TestPublishThread_RecordErrorStops(t *testing.T) {
	pds := newFakePDS(t)
	p, _ := pds.publisher(t, "")
	boom := errors.New("disk full")
	_, err := p.PublishThread(context.Background(), testArticle, threadPayload("Uno.", "Dos."), nil,
		func(publishing.PostRef) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the record error", err)
	}
	if n := pds.count("com.atproto.repo.createRecord"); n != 1 {
		t.Fatalf("createRecord calls = %d, want 1", n)
	}
}

func TestPublishThread_AmbiguousPartIsNotRecorded(t *testing.T) {
	pds := newFakePDS(t)
	var n atomic.Int32
	pds.createRecord = func(w http.ResponseWriter, r *http.Request) bool {
		if n.Add(1) == 2 {
			hijackClose(w)
			return true
		}
		return false
	}
	p, _ := pds.publisher(t, "")
	var recorded []publishing.PostRef
	_, err := p.PublishThread(context.Background(), testArticle, threadPayload("Uno.", "Dos.", "Tres."), nil,
		func(ref publishing.PostRef) error { recorded = append(recorded, ref); return nil })
	var amb *appout.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("error = %v, want AmbiguousError", err)
	}
	if len(recorded) != 1 || recorded[0] != postedRef(1) {
		t.Fatalf("recorded = %v, want only the first part", recorded)
	}
	if c := pds.count("com.atproto.repo.createRecord"); c != 2 {
		t.Fatalf("createRecord calls = %d, want 2", c)
	}
}

func replyFeedPost(uri, cid, text string, createdAt time.Time, parentURI string) string {
	return fmt.Sprintf(`{"post":{"uri":%q,"cid":%q,"author":{"handle":%q},"record":{"text":%q,"createdAt":%q,"reply":{"root":{"uri":%q,"cid":"x"},"parent":{"uri":%q,"cid":"y"}}}}}`,
		uri, cid, testHandle, text, createdAt.UTC().Format("2006-01-02T15:04:05.000Z"), parentURI, parentURI)
}

func TestFindSent_ThreadPartMatchesParent(t *testing.T) {
	sentAt := baseTime
	progress := []publishing.PostRef{{URI: "at://d/p/1", CID: "c1"}}
	tests := []struct {
		name        string
		feed        string
		want        publishing.PostRef
		wantMatched bool
		wantCovered bool
	}{
		{"reply to the last part matches", feedJSON(
			replyFeedPost("at://d/p/3", "c3", "Dos.", sentAt.Add(time.Second), "at://d/p/other"),
			replyFeedPost("at://d/p/2", "c2", "Dos.", sentAt.Add(time.Second), "at://d/p/1"),
			feedPost("at://d/p/1", testHandle, "Uno.", sentAt.Add(-time.Hour))),
			publishing.PostRef{URI: "at://d/p/2", CID: "c2"}, true, true},
		{"same text under another parent does not match", feedJSON(
			replyFeedPost("at://d/p/3", "c3", "Dos.", sentAt.Add(time.Second), "at://d/p/other"),
			feedPost("at://d/p/1", testHandle, "Uno.", sentAt.Add(-time.Hour))),
			publishing.PostRef{}, false, true},
		{"top-level post with the text does not match", feedJSON(
			feedPost("at://d/p/3", testHandle, "Dos.", sentAt.Add(time.Second)),
			feedPost("at://d/p/1", testHandle, "Uno.", sentAt.Add(-time.Hour))),
			publishing.PostRef{}, false, true},
		{"shallow feed is inconclusive", feedJSON(
			replyFeedPost("at://d/p/3", "c3", "Dos.", sentAt.Add(time.Second), "at://d/p/other")),
			publishing.PostRef{}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pds := newFakePDS(t)
			var query string
			pds.feed = func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				writeJSON(w, 200, tt.feed)
			}
			p, _ := pds.publisher(t, "")
			ref, matched, covered, err := p.FindSent(context.Background(), appout.SentLookup{
				Article: testArticle, Payload: threadPayload("Uno.", "Dos.", "Tres."),
				SentAt: sentAt, ClockSkew: time.Minute, ThreadProgress: progress,
			})
			if err != nil {
				t.Fatalf("FindSent() error = %v", err)
			}
			if ref != tt.want || matched != tt.wantMatched || covered != tt.wantCovered {
				t.Fatalf("FindSent() = (%v, %v, %v), want (%v, %v, %v)", ref, matched, covered, tt.want, tt.wantMatched, tt.wantCovered)
			}
			if strings.Contains(query, "posts_no_replies") {
				t.Errorf("query = %q, want replies included", query)
			}
		})
	}
}

func TestFindSent_ThreadRootReturnsCID(t *testing.T) {
	pds := newFakePDS(t)
	pds.feed = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, feedJSON(`{"post":{"uri":"at://d/p/1","cid":"c1","author":{"handle":"`+testHandle+`"},"record":{"text":"Uno.","createdAt":"`+baseTime.Format("2006-01-02T15:04:05.000Z")+`"}}}`))
	}
	p, _ := pds.publisher(t, "")
	ref, matched, _, err := p.FindSent(context.Background(), appout.SentLookup{
		Article: testArticle, Payload: threadPayload("Uno.", "Dos."), SentAt: baseTime, ClockSkew: time.Minute,
	})
	if err != nil || !matched || ref != (publishing.PostRef{URI: "at://d/p/1", CID: "c1"}) {
		t.Fatalf("FindSent() = (%v, %v, %v)", ref, matched, err)
	}
}
