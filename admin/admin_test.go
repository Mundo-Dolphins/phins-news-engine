package admin_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Mundo-Dolphins/phins-news-engine/admin"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

const (
	user = "admin"
	pass = "s3cret"
)

var (
	jobStarted  = make(chan struct{}, 1)
	jobFinished atomic.Bool
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// A host action that outlives its request, to observe shutdown ordering.
	admin.Add(action.Action{
		Name: "test.slow", Group: "test", Title: "Slow", Kind: action.KindWrite,
		Modes: []action.Mode{action.ModeLocal}, LongRunning: true,
		Run: func(ctx context.Context, _ action.Env, _ any) (action.Result, error) {
			jobStarted <- struct{}{}
			<-ctx.Done()
			time.Sleep(200 * time.Millisecond)
			jobFinished.Store(true)
			return action.Result{}, ctx.Err()
		},
	})
	os.Exit(m.Run())
}

type harness struct {
	t      *testing.T
	opPath string
	opts   admin.Options
	bases  []string
	cancel context.CancelFunc
	done   chan error
}

// newHarness prepares config, env and listeners; start launches Run.
func newHarness(t *testing.T, profile string, nListeners int) *harness {
	t.Helper()
	dir := t.TempDir()
	opPath := filepath.Join(dir, "op.db")
	raw, err := os.ReadFile(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgText := strings.Replace(string(raw), "path: data/operational.db", "path: "+opPath, 1)
	cfgPath := filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	t.Setenv("ADMIN_USERNAME", user)
	t.Setenv("ADMIN_PASSWORD_HASH", string(hash))
	t.Setenv("ADMIN_MCP_TOKEN", strings.Repeat("t", 40))
	t.Setenv("ADMIN_DB_PATH", filepath.Join(dir, "admin.db"))

	h := &harness{t: t, opPath: opPath, done: make(chan error, 1)}
	h.opts = admin.Options{ConfigPath: cfgPath, Profile: profile}
	for i := 0; i < nListeners; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		h.opts.Listeners = append(h.opts.Listeners, ln)
		h.bases = append(h.bases, "http://"+ln.Addr().String())
	}
	return h
}

func (h *harness) migrateOp(mutate func(*sql.DB)) {
	h.t.Helper()
	db, err := infrasqlite.Open(h.opPath, 5000)
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		h.t.Fatal(err)
	}
	if mutate != nil {
		mutate(db)
	}
}

func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- admin.Run(ctx, h.opts) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case err := <-h.done:
			_ = err
		case <-time.After(15 * time.Second):
		}
	})
	for _, b := range h.bases {
		h.waitUp(b)
	}
}

func (h *harness) waitUp(base string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-h.done:
			h.t.Fatalf("Run returned early: %v", err)
		default:
		}
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("%s never came up", base)
}

func (h *harness) stop() error {
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err // let the cleanup see that Run already returned
		return err
	case <-time.After(15 * time.Second):
		h.t.Fatal("Run did not return")
		return nil
	}
}

func getJSON(t *testing.T, url string) map[string]string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func loggedIn(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	req, _ := http.NewRequest("POST", base+"/api/session", strings.NewReader(`{"username":"admin","password":"s3cret"}`))
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	return c
}

func postAction(t *testing.T, c *http.Client, base, name, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/actions/"+name, strings.NewReader(body))
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func versions(t *testing.T, path string) []string {
	t.Helper()
	db, err := infrasqlite.Open(path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestRunServesHealthzOnAllListenAddrs(t *testing.T) {
	h := newHarness(t, "local", 2)
	h.start()
	for _, b := range h.bases {
		m := getJSON(t, b+"/healthz")
		if m["status"] != "ok" || m["mode"] != "local" || m["schema"] != "ok" {
			t.Fatalf("%s healthz = %v", b, m)
		}
	}
	if err := h.stop(); err != nil {
		t.Fatalf("Run = %v, want nil on clean shutdown", err)
	}
}

func TestLocalMigrates(t *testing.T) {
	h := newHarness(t, "local", 1)
	h.start()
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if got := versions(t, h.opPath); len(got) == 0 {
		t.Fatal("local mode did not migrate the operational DB")
	}
}

func dropLastMigration(db *sql.DB) {
	_, _ = db.Exec(`DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`)
}

func TestProductionDoesNotMigrate(t *testing.T) {
	h := newHarness(t, "production", 1)
	h.migrateOp(dropLastMigration)
	before := versions(t, h.opPath)
	h.start()
	m := getJSON(t, h.bases[0]+"/healthz")
	if m["mode"] != "production" || m["schema"] != "mismatch" {
		t.Fatalf("healthz = %v", m)
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if after := versions(t, h.opPath); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("production migrated: before %v after %v", before, after)
	}
}

func TestProductionSchemaMismatchBlocksWrites(t *testing.T) {
	cases := map[string]func(*sql.DB){
		"behind": dropLastMigration,
		"ahead": func(db *sql.DB) {
			_, _ = db.Exec(`INSERT INTO schema_migrations(version) VALUES ('9999_from_the_future.sql')`)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "production", 1)
			h.migrateOp(mutate)
			h.start()
			c := loggedIn(t, h.bases[0])
			code, body := postAction(t, c, h.bases[0], "claims.release", `{"params":{"handler":"h","key":"k"},"confirm":true}`)
			if code != http.StatusServiceUnavailable || !strings.Contains(body, action.CodeSchemaMismatch) ||
				!strings.Contains(body, "operational DB schema differs from this binary") {
				t.Fatalf("release = %d %s", code, body)
			}
			if code, body := postAction(t, c, h.bases[0], "claims.list", `{}`); code != http.StatusOK {
				t.Fatalf("list = %d %s", code, body)
			}
		})
	}
}

func TestInvalidAdminEnvFailsFast(t *testing.T) {
	h := newHarness(t, "local", 1)
	t.Setenv("ADMIN_USERNAME", "")
	err := admin.Run(context.Background(), h.opts)
	if err == nil || !strings.Contains(err.Error(), "ADMIN_USERNAME") {
		t.Fatalf("Run = %v, want ADMIN_USERNAME error", err)
	}
}

func TestBadListenAddrFailsBeforeServing(t *testing.T) {
	h := newHarness(t, "local", 0)
	t.Setenv("ADMIN_LISTEN_ADDRS", "127.0.0.1:99999")
	if err := admin.Run(context.Background(), h.opts); err == nil {
		t.Fatal("Run succeeded with an unusable listen address")
	}
}

func TestGracefulShutdownWaitsForJob(t *testing.T) {
	jobFinished.Store(false)
	h := newHarness(t, "local", 1)
	h.start()
	c := loggedIn(t, h.bases[0])
	if code, body := postAction(t, c, h.bases[0], "test.slow", `{}`); code != http.StatusAccepted {
		t.Fatalf("start = %d %s", code, body)
	}
	select {
	case <-jobStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("job never started")
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if !jobFinished.Load() {
		t.Fatal("Run returned before the running job finished")
	}
}
