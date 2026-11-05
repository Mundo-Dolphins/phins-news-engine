package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/httpapi"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

const (
	user = "admin"
	pass = "s3cret"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type env struct {
	srv      *httptest.Server
	client   *http.Client
	deps     *httpapi.Deps
	store    *store.Store
	gate     chan struct{}
	spamGate chan struct{}
}

type readParams struct {
	N int `json:"n" default:"1"`
}

func newEnv(t *testing.T, mutate func(*httpapi.Deps)) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := jobs.New(ctx, st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); runner.Wait(); _ = st.Close() })

	gate := make(chan struct{})
	spamGate := make(chan struct{})
	reg := &action.Registry{}
	reg.Add(
		action.Action{Name: "t.read", Group: "t", Title: "Read", Kind: action.KindRead,
			Modes: []action.Mode{action.ModeLocal, action.ModeProduction}, Params: readParams{},
			Run: func(_ context.Context, _ action.Env, p any) (action.Result, error) {
				return action.Result{Render: "text", Text: "read ok"}, nil
			}},
		action.Action{Name: "t.fail", Group: "t", Kind: action.KindRead,
			Modes: []action.Mode{action.ModeLocal},
			Run: func(context.Context, action.Env, any) (action.Result, error) {
				return action.Result{}, errors.New("secret internals")
			}},
		action.Action{Name: "t.destroy", Group: "t", Kind: action.KindDestructive,
			Modes: []action.Mode{action.ModeLocal},
			Run: func(context.Context, action.Env, any) (action.Result, error) {
				return action.Result{Render: "text", Text: "destroyed"}, nil
			}},
		action.Action{Name: "t.wait", Group: "t", Kind: action.KindWrite, LongRunning: true,
			Modes: []action.Mode{action.ModeLocal},
			Run: func(ctx context.Context, _ action.Env, _ any) (action.Result, error) {
				<-ctx.Done()
				return action.Result{}, ctx.Err()
			}},
		action.Action{Name: "t.spam", Group: "t", Kind: action.KindWrite, LongRunning: true,
			Modes: []action.Mode{action.ModeLocal},
			Run: func(_ context.Context, e action.Env, _ any) (action.Result, error) {
				<-spamGate
				for i := 0; i < 400; i++ {
					e.Log.Info("spam")
				}
				return action.Result{Render: "text"}, nil
			}},
		action.Action{Name: "t.long", Group: "t", Kind: action.KindWrite, LongRunning: true,
			Modes: []action.Mode{action.ModeLocal},
			Run: func(ctx context.Context, e action.Env, _ any) (action.Result, error) {
				e.Log.Info("step one")
				<-gate
				e.Log.Info("step two")
				return action.Result{Render: "text", Text: "long done"}, nil
			}},
	)
	hb, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost) // fast; CheckPassword accepts any cost
	if err != nil {
		t.Fatal(err)
	}
	hash := string(hb)
	d := httpapi.Deps{
		Registry: reg, Runner: runner, Store: st,
		Sessions: auth.NewSessions(st, time.Hour, time.Now),
		Limiter:  auth.NewLoginLimiter(5, time.Minute, time.Now),
		EnvFor:   func(actor string) action.Env { return action.Env{Actor: actor, Mode: action.ModeLocal} },
		Username: user, PasswordHash: hash,
		Assets: fstest.MapFS{
			"index.html":    {Data: []byte("<html>INDEX</html>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
		Mode: action.ModeLocal,
	}
	if mutate != nil {
		mutate(&d)
	}
	allowed := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	srv := httptest.NewServer(httpapi.NewHandler(d, allowed, "tok"))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &env{srv: srv, client: &http.Client{Jar: jar}, deps: &d, store: st, gate: gate, spamGate: spamGate}
}

func (e *env) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Origin", e.srv.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) login(t *testing.T) {
	t.Helper()
	resp, b := e.do(t, "POST", "/api/session", map[string]string{"username": user, "password": pass})
	if resp.StatusCode != 204 {
		t.Fatalf("login = %d %s", resp.StatusCode, b)
	}
}

func errCode(t *testing.T, b []byte) string {
	t.Helper()
	var v struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("bad error body %q: %v", b, err)
	}
	return v.Error.Code
}

func TestUnauthenticatedAPIIs401(t *testing.T) {
	e := newEnv(t, nil)
	resp, b := e.do(t, "GET", "/api/actions", nil)
	if resp.StatusCode != 401 || errCode(t, b) != "unauthorized" {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestHealthzNoAuth(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) {
		blocked := &action.Error{Code: action.CodeSchemaMismatch, Message: "behind"}
		d.WritesBlocked = func() error { return blocked }
	})
	resp, b := e.do(t, "GET", "/healthz", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"schema":"mismatch"`) || !strings.Contains(string(b), `"mode":"local"`) {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestLoginSetsCookie(t *testing.T) {
	e := newEnv(t, nil)
	resp, _ := e.do(t, "POST", "/api/session", map[string]string{"username": user, "password": pass})
	if resp.StatusCode != 204 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName && c.HttpOnly {
			found = true
		}
	}
	if !found {
		t.Fatal("no session cookie")
	}
	_, b := e.do(t, "GET", "/api/session", nil)
	if !strings.Contains(string(b), `"username":"admin"`) || !strings.Contains(string(b), `"mode":"local"`) {
		t.Fatalf("session body %s", b)
	}
	if resp, _ := e.do(t, "DELETE", "/api/session", nil); resp.StatusCode != 204 {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/actions", nil); resp.StatusCode != 401 {
		t.Fatalf("after logout %d", resp.StatusCode)
	}
}

func TestLoginWrongPasswordIs401(t *testing.T) {
	e := newEnv(t, nil)
	resp, _ := e.do(t, "POST", "/api/session", map[string]string{"username": user, "password": "nope"})
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := newEnv(t, nil)
	for i := 0; i < 5; i++ {
		resp, _ := e.do(t, "POST", "/api/session", map[string]string{"username": user, "password": "bad"})
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp, b := e.do(t, "POST", "/api/session", map[string]string{"username": user, "password": pass})
	if resp.StatusCode != 429 {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestCatalogFiltersByMode(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	_, b := e.do(t, "GET", "/api/actions", nil)
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("local catalog = %d: %s", len(items), b)
	}
	for _, k := range []string{"name", "group", "title", "description", "kind", "modes", "long_running", "exclusive", "schema"} {
		if _, ok := items[0][k]; !ok {
			t.Errorf("missing key %q in %v", k, items[0])
		}
	}

	p := newEnv(t, func(d *httpapi.Deps) { d.Mode = action.ModeProduction })
	p.login(t)
	_, b = p.do(t, "GET", "/api/actions", nil)
	items = nil
	_ = json.Unmarshal(b, &items)
	if len(items) != 1 || items[0]["name"] != "t.read" {
		t.Fatalf("production catalog: %s", b)
	}
	resp, b := p.do(t, "POST", "/api/actions/t.destroy", map[string]any{"confirm": true})
	if resp.StatusCode != 404 || errCode(t, b) != "not_found" {
		t.Fatalf("out-of-mode action: %d %s", resp.StatusCode, b)
	}
}

func TestRunReadAndInvalidParams(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	resp, b := e.do(t, "POST", "/api/actions/t.read", map[string]any{"params": map[string]any{"n": 2}})
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"result":{"render":"text","text":"read ok"}`) {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	resp, b = e.do(t, "POST", "/api/actions/t.read", map[string]any{"params": map[string]any{"bogus": 2}})
	if resp.StatusCode != 400 || errCode(t, b) != "invalid_params" {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	resp, b = e.do(t, "POST", "/api/actions/nope", map[string]any{})
	if resp.StatusCode != 404 {
		t.Fatalf("unknown action: %d %s", resp.StatusCode, b)
	}
}

func TestInternalErrorIsGeneric(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	resp, b := e.do(t, "POST", "/api/actions/t.fail", map[string]any{})
	if resp.StatusCode != 500 || errCode(t, b) != "internal" || strings.Contains(string(b), "secret") {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestDestructiveWithoutConfirmIs400(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	resp, b := e.do(t, "POST", "/api/actions/t.destroy", map[string]any{})
	if resp.StatusCode != 400 || errCode(t, b) != "confirmation_required" {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	resp, _ = e.do(t, "POST", "/api/actions/t.destroy", map[string]any{"confirm": true})
	if resp.StatusCode != 200 {
		t.Fatalf("confirmed: %d", resp.StatusCode)
	}
}

func TestWriteBlockedBySchemaMismatchIs503(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) {
		blocked := &action.Error{Code: action.CodeSchemaMismatch, Message: "schema is behind"}
		d.WritesBlocked = func() error { return blocked }
	})
	e.login(t)
	for _, name := range []string{"t.destroy", "t.long"} {
		resp, b := e.do(t, "POST", "/api/actions/"+name, map[string]any{"confirm": true})
		if resp.StatusCode != 503 || errCode(t, b) != "schema_mismatch" || !strings.Contains(string(b), "schema is behind") {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
}

func TestReadAllowedDuringSchemaMismatch(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) {
		blocked := &action.Error{Code: action.CodeSchemaMismatch, Message: "x"}
		d.WritesBlocked = func() error { return blocked }
	})
	e.login(t)
	if resp, b := e.do(t, "POST", "/api/actions/t.read", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func startLong(t *testing.T, e *env) store.Job { return startJob(t, e, "t.long") }

func startJob(t *testing.T, e *env, name string) store.Job {
	t.Helper()
	resp, b := e.do(t, "POST", "/api/actions/"+name, map[string]any{})
	if resp.StatusCode != 202 {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	var raw struct {
		Job struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Actor  string `json:"actor"`
		} `json:"job"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.Job.ID == "" {
		t.Fatalf("bad job %s", b)
	}
	if raw.Job.Actor != "user:admin" {
		t.Fatalf("actor %q", raw.Job.Actor)
	}
	return store.Job{ID: raw.Job.ID}
}

func waitStatus(t *testing.T, e *env, id, want string) []byte {
	t.Helper()
	for i := 0; i < 200; i++ {
		_, b := e.do(t, "GET", "/api/jobs/"+id, nil)
		var j struct{ Status string }
		_ = json.Unmarshal(b, &j)
		if j.Status == want {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, want)
	return nil
}

func TestLongRunningReturns202AndCompletes(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	j := startLong(t, e)
	close(e.gate)
	b := waitStatus(t, e, j.ID, "succeeded")
	for _, k := range []string{`"created_at"`, `"started_at"`, `"finished_at"`, `"long done"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("job JSON lacks %s: %s", k, b)
		}
	}
	_, b = e.do(t, "GET", "/api/jobs?limit=5", nil)
	if !strings.Contains(string(b), j.ID) {
		t.Fatalf("list lacks job: %s", b)
	}
	_, b = e.do(t, "GET", "/api/audit?limit=5", nil)
	if !strings.Contains(string(b), `"t.long"`) || !strings.Contains(string(b), `"outcome"`) {
		t.Fatalf("audit: %s", b)
	}
	if resp, b := e.do(t, "GET", "/api/jobs/nonexistent", nil); resp.StatusCode != 404 {
		t.Fatalf("missing job: %d %s", resp.StatusCode, b)
	}
}

func TestCancelJob(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	j := startJob(t, e, "t.wait")
	if resp, b := e.do(t, "POST", "/api/jobs/"+j.ID+"/cancel", nil); resp.StatusCode != 202 {
		t.Fatalf("cancel: %d %s", resp.StatusCode, b)
	}
	waitStatus(t, e, j.ID, "cancelled")
	resp, b := e.do(t, "POST", "/api/jobs/"+j.ID+"/cancel", nil)
	if resp.StatusCode != 409 || errCode(t, b) != "conflict" {
		t.Fatalf("cancel finished: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "POST", "/api/jobs/missing/cancel", nil); resp.StatusCode != 404 {
		t.Fatalf("cancel missing: %d", resp.StatusCode)
	}
}

func TestSSENoGapsWithManyLines(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	j := startJob(t, e, "t.spam")
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/jobs/"+j.ID+"/events", nil)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	close(e.spamGate)
	waitStatus(t, e, j.ID, "succeeded") // client has not read anything yet
	rd := bufio.NewReader(resp.Body)
	evs := readSSE(t, rd, 401)
	for i := 0; i < 400; i++ {
		if evs[i].event != "log" || evs[i].id != strconv.Itoa(i+1) {
			t.Fatalf("event %d = %+v", i, evs[i])
		}
	}
	if evs[400].event != "end" {
		t.Fatalf("last = %+v", evs[400])
	}
}

type sseEvent struct{ id, event, data string }

func readSSE(t *testing.T, r *bufio.Reader, n int) []sseEvent {
	t.Helper()
	var out []sseEvent
	cur := sseEvent{}
	for len(out) < n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("sse read: %v (got %v)", err, out)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if cur.event != "" {
				out = append(out, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
	return out
}

func TestSSEStreamsLogsAndEnd(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	j := startLong(t, e)
	// wait for the first log line to be persisted
	for i := 0; i < 200; i++ {
		ls, _ := e.store.Logs(context.Background(), j.ID, 0)
		if len(ls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/jobs/"+j.ID+"/events", nil)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	rd := bufio.NewReader(resp.Body)
	first := readSSE(t, rd, 1)
	if first[0].event != "log" || first[0].id == "" || !strings.Contains(first[0].data, `"line":"step one`) || !strings.Contains(first[0].data, `"seq"`) {
		t.Fatalf("first event %+v", first[0])
	}
	close(e.gate)
	rest := readSSE(t, rd, 2)
	if rest[0].event != "log" || !strings.Contains(rest[0].data, "step two") {
		t.Fatalf("second %+v", rest[0])
	}
	if rest[1].event != "end" || !strings.Contains(rest[1].data, `"status":"succeeded"`) {
		t.Fatalf("end %+v", rest[1])
	}

	// finished job: history replay honouring Last-Event-ID, then end.
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/jobs/"+j.ID+"/events", nil)
	req.Header.Set("Last-Event-ID", first[0].id)
	resp2, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	evs := readSSE(t, bufio.NewReader(resp2.Body), 2)
	if evs[0].event != "log" || !strings.Contains(evs[0].data, "step two") || evs[1].event != "end" {
		t.Fatalf("replay %+v", evs)
	}
}

func TestCrossOriginPostIs403(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/actions/t.read", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://evil.example")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestBodyLimit(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	big := map[string]any{"params": map[string]any{"n": strings.Repeat("x", 2<<20)}}
	resp, b := e.do(t, "POST", "/api/actions/t.read", big)
	if resp.StatusCode != 413 || errCode(t, b) != "payload_too_large" {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	e := newEnv(t, nil)
	resp, b := e.do(t, "GET", "/actions/claims.list", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "INDEX") {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("cache-control %q", resp.Header.Get("Cache-Control"))
	}
	resp, b = e.do(t, "GET", "/assets/app.js", nil)
	if resp.StatusCode != 200 || string(b) != "console.log(1)" || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("asset: %d %q %q", resp.StatusCode, b, resp.Header.Get("Content-Type"))
	}
	if resp, _ := e.do(t, "GET", "/assets/old.js", nil); resp.StatusCode != 404 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	resp, b = e.do(t, "GET", "/", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "INDEX") {
		t.Fatalf("root: %d %s", resp.StatusCode, b)
	}
}

func TestUnknownAPIIsJSON404(t *testing.T) {
	e := newEnv(t, nil)
	e.login(t)
	resp, b := e.do(t, "GET", "/api/nope", nil)
	if resp.StatusCode != 404 || errCode(t, b) != "not_found" {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}

func TestMCPMountRequiresBearer(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) {
		d.MCP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "mcp!") })
	})
	resp, _ := e.do(t, "POST", "/mcp", map[string]any{})
	if resp.StatusCode != 401 {
		t.Fatalf("no bearer: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || string(b) != "mcp!" {
		t.Fatalf("with bearer: %d %s", r.StatusCode, b)
	}
}

func TestOutsideLANIs403(t *testing.T) {
	e := newEnv(t, nil)
	h := httpapi.NewHandler(*e.deps, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, "tok")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestWriteBlockedPlainErrorIs503(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) { d.WritesBlocked = func() error { return errors.New("db behind") } })
	e.login(t)
	resp, b := e.do(t, "POST", "/api/actions/t.long", map[string]any{})
	if resp.StatusCode != 503 || errCode(t, b) != "schema_mismatch" || !strings.Contains(string(b), "db behind") {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
}
