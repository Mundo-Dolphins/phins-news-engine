package cycle_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/cycle"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

type fixture struct {
	t         *testing.T
	cfg       *config.Config
	db        *sql.DB
	storePath string
	st        *store.Store
	remote    action.Remote
	swapped   []*sql.DB
	log       *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	overridden := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overridden, filepath.Join(dir, "models", "editorial", "current"))
	cfg, err := config.Load(overridden)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.OpenImmediate(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, cfg: cfg, db: db, storePath: filepath.Join(t.TempDir(), "admin.db"), log: &bytes.Buffer{}}
	f.openStore()
	t.Cleanup(func() {
		_ = f.db.Close()
		_ = f.st.Close()
	})
	return f
}

func (f *fixture) openStore() {
	f.t.Helper()
	st, err := store.Open(context.Background(), f.storePath)
	if err != nil {
		f.t.Fatal(err)
	}
	f.st = st
}

func (f *fixture) env() action.Env {
	return action.Env{
		DB: f.db, Config: f.cfg, Mode: action.ModeLocal, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana", State: f.st, Remote: f.remote,
		SwapDB: func(db *sql.DB) {
			f.swapped = append(f.swapped, db)
			f.db = db
		},
	}
}

func (f *fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range cycle.Actions() {
		if c.Name == name {
			a = c
		}
	}
	if a.Name == "" {
		f.t.Fatalf("no action %q", name)
	}
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	return a.Run(context.Background(), f.env(), p)
}

func (f *fixture) mustRun(name, params string) action.Result {
	f.t.Helper()
	res, err := f.run(name, params)
	if err != nil {
		f.t.Fatalf("%s(%s): %v", name, params, err)
	}
	return res
}

// view is the JSON shape of cycle.state (and of cycle.start / cycle.next).
type view struct {
	Cycle *struct {
		CycleID   string    `json:"cycle_id"`
		StartedAt time.Time `json:"started_at"`
		Step      string    `json:"step"`
		SessionID string    `json:"session_id"`
		Steps     map[string]struct {
			Status    string          `json:"status"`
			Result    json.RawMessage `json:"result"`
			Error     string          `json:"error"`
			UpdatedAt time.Time       `json:"updated_at"`
		} `json:"steps"`
		SampleSize           int    `json:"sample_size"`
		EntitiesVerification string `json:"entities_verification"`
	} `json:"cycle"`
	Available struct {
		Sync     bool `json:"sync"`
		Telegram bool `json:"telegram"`
	} `json:"available"`
	Order     []string          `json:"order"`
	Skippable []string          `json:"skippable"`
	Disabled  map[string]string `json:"disabled"`
}

func decode[T any](t *testing.T, res action.Result) T {
	t.Helper()
	raw, err := json.Marshal(res.Object)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func (f *fixture) state() view {
	f.t.Helper()
	return decode[view](f.t, f.mustRun("cycle.state", `{}`))
}

// setState edits the stored wizard state directly, so a test can place the
// cycle at a later step.
func (f *fixture) setState(edit func(m map[string]any)) {
	f.t.Helper()
	raw, ok, err := f.st.GetState(context.Background(), "editorial_cycle_wizard")
	if err != nil || !ok {
		f.t.Fatalf("no wizard state: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		f.t.Fatal(err)
	}
	edit(m)
	raw, _ = json.Marshal(m)
	if err := f.st.PutState(context.Background(), "editorial_cycle_wizard", raw); err != nil {
		f.t.Fatal(err)
	}
}

func stepsMap(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	steps, ok := m["steps"].(map[string]any)
	if !ok {
		t.Fatalf("state has no steps: %v", m)
	}
	return steps
}

func stepMap(t *testing.T, m map[string]any, name string) map[string]any {
	t.Helper()
	s, ok := stepsMap(t, m)[name].(map[string]any)
	if !ok {
		t.Fatalf("state has no step %s", name)
	}
	return s
}

func (f *fixture) atStep(step string) {
	f.setState(func(m map[string]any) { m["step"] = step })
}

func wantConflict(t *testing.T, err error, contains string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeConflict {
		t.Fatalf("err = %v, want conflict", err)
	}
	if !strings.Contains(ae.Message, contains) {
		t.Fatalf("conflict = %q, want it to contain %q", ae.Message, contains)
	}
}

func TestMetadata(t *testing.T) {
	want := map[string]struct {
		kind                         action.Kind
		long, exclusive, swaps, read bool
	}{
		"cycle.state":           {kind: action.KindRead},
		"cycle.start":           {kind: action.KindWrite},
		"cycle.next":            {kind: action.KindWrite},
		"cycle.abandon":         {kind: action.KindDestructive},
		"cycle.sync":            {kind: action.KindWrite, long: true, exclusive: true, swaps: true},
		"cycle.telegram_import": {kind: action.KindWrite, long: true},
		"cycle.roster_sync":     {kind: action.KindWrite, long: true},
		"cycle.train":           {kind: action.KindWrite, long: true, exclusive: true},
		"cycle.promote":         {kind: action.KindDestructive},
		"cycle.summary":         {kind: action.KindRead},
	}
	got := map[string]bool{}
	for _, a := range cycle.Actions() {
		w, ok := want[a.Name]
		if !ok {
			t.Errorf("unexpected action %s", a.Name)
			continue
		}
		got[a.Name] = true
		if a.Group != "cycle" || a.Kind != w.kind || a.LongRunning != w.long || a.Exclusive != w.exclusive || a.SwapsDB != w.swaps {
			t.Errorf("%s: group=%s kind=%s long=%v exclusive=%v swaps=%v", a.Name, a.Group, a.Kind, a.LongRunning, a.Exclusive, a.SwapsDB)
		}
		if len(a.Modes) != 1 || a.Modes[0] != action.ModeLocal {
			t.Errorf("%s: modes = %v, want local only", a.Name, a.Modes)
		}
	}
	if len(got) != len(want) {
		t.Errorf("actions = %v, want %d", got, len(want))
	}
}

func TestDefaultSyncScriptIsTheCLIs(t *testing.T) {
	if action.DefaultSyncScriptPath != shared.DefaultRemoteSyncScriptPath {
		t.Errorf("DefaultSyncScriptPath = %q, want the CLI's %q", action.DefaultSyncScriptPath, shared.DefaultRemoteSyncScriptPath)
	}
}

func TestNoCycle(t *testing.T) {
	f := newFixture(t)
	v := f.state()
	if v.Cycle != nil {
		t.Fatalf("cycle = %+v, want null", v.Cycle)
	}
	_, err := f.run("cycle.next", `{}`)
	wantConflict(t, err, "no editorial cycle")
	_, err = f.run("cycle.abandon", `{}`)
	wantConflict(t, err, "no editorial cycle")
}

func TestStartValidatesAndRefusesSecondCycle(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run("cycle.start", `{"entities_verification":"deep"}`); err == nil {
		t.Fatal("bad entities_verification accepted")
	}
	if _, err := f.run("cycle.start", `{"sample_size":-1}`); err == nil {
		t.Fatal("negative sample_size accepted")
	}
	v := decode[view](t, f.mustRun("cycle.start", `{"sample_size":12}`))
	if v.Cycle == nil || v.Cycle.Step != "sync" || v.Cycle.SampleSize != 12 || v.Cycle.EntitiesVerification != "local" || v.Cycle.CycleID == "" {
		t.Fatalf("start = %+v", v.Cycle)
	}
	if !v.Cycle.StartedAt.Equal(now) {
		t.Errorf("started_at = %v, want %v", v.Cycle.StartedAt, now)
	}
	wantOrder := "sync,telegram_import,roster_sync,article_review,entities,event_rules,mined_terms,training,summary"
	if got := strings.Join(v.Order, ","); got != wantOrder {
		t.Errorf("order = %s", got)
	}
	for _, s := range v.Order {
		if v.Cycle.Steps[s].Status != "pending" {
			t.Errorf("step %s = %+v, want pending", s, v.Cycle.Steps[s])
		}
	}
	_, err := f.run("cycle.start", `{}`)
	wantConflict(t, err, "already in progress")
}

func TestStateRoundTripAcrossRestart(t *testing.T) {
	f := newFixture(t)
	started := decode[view](t, f.mustRun("cycle.start", `{"entities_verification":"none"}`))
	f.mustRun("cycle.next", `{"skip":true}`)
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	f.openStore() // a new admin process: same store file, fresh Env
	v := f.state()
	if v.Cycle == nil || v.Cycle.CycleID != started.Cycle.CycleID || v.Cycle.Step != "telegram_import" ||
		v.Cycle.EntitiesVerification != "none" {
		t.Fatalf("after restart = %+v", v.Cycle)
	}
	if s := v.Cycle.Steps["sync"]; s.Status != "skipped" {
		t.Errorf("sync = %+v, want skipped", s)
	}
}

func TestNextRefusedUntilDone(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	_, err := f.run("cycle.next", `{}`)
	wantConflict(t, err, "not finished")
	f.setState(func(m map[string]any) {
		stepMap(t, m, "sync")["status"] = "failed"
	})
	_, err = f.run("cycle.next", `{}`)
	wantConflict(t, err, "not finished")
	f.setState(func(m map[string]any) {
		stepMap(t, m, "sync")["status"] = "done"
	})
	if v := decode[view](t, f.mustRun("cycle.next", `{}`)); v.Cycle.Step != "telegram_import" {
		t.Fatalf("step = %s, want telegram_import", v.Cycle.Step)
	}
}

func TestSkipOnlySkippable(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	f.mustRun("cycle.next", `{"skip":true}`) // sync
	v := decode[view](t, f.mustRun("cycle.next", `{"skip":true}`))
	tg := v.Cycle.Steps["telegram_import"]
	if tg.Status != "skipped" {
		t.Fatalf("telegram_import = %+v", tg)
	}
	var tgResult struct {
		SkipReason string `json:"skip_reason"`
		Warning    string `json:"warning"`
	}
	if err := json.Unmarshal(tg.Result, &tgResult); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tgResult.Warning, "WARNING: --skip-telegram-import is set — this session's publications will be reconstructed without Telegram history.") {
		t.Errorf("telegram warning = %q", tgResult.Warning)
	}
	v = decode[view](t, f.mustRun("cycle.next", `{"skip":true}`)) // roster_sync
	if v.Cycle.Step != "article_review" {
		t.Fatalf("step = %s, want article_review", v.Cycle.Step)
	}
	_, err := f.run("cycle.next", `{"skip":true}`)
	wantConflict(t, err, "cannot be skipped")
	if strings.Join(v.Skippable, ",") != "sync,telegram_import,roster_sync,entities,event_rules,mined_terms,training" {
		t.Errorf("skippable = %v", v.Skippable)
	}
}

func TestStepActionWrongStepConflict(t *testing.T) {
	f := newFixture(t)
	f.remote = action.Remote{Host: "box.example", NewSync: func(string, string) out.RemoteSync { return nil },
		NewTelegramClient: func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
			return nil, errors.New("unused")
		}}
	_, err := f.run("cycle.sync", `{}`)
	wantConflict(t, err, "no editorial cycle")
	f.mustRun("cycle.start", `{}`)
	for name, params := range map[string]string{
		"cycle.telegram_import": `{}`, "cycle.roster_sync": `{}`, "cycle.train": `{}`,
		"cycle.promote": `{"promote":true}`, "cycle.summary": `{}`,
	} {
		_, err := f.run(name, params)
		wantConflict(t, err, "the cycle is at step sync")
	}
	f.atStep("training")
	_, err = f.run("cycle.sync", `{}`)
	wantConflict(t, err, "the cycle is at step training")
}

// fakeSync serves a prepared snapshot file.
type fakeSync struct {
	path, checksum string
	size           int64
	req            out.RemoteSyncRequest
}

func (s *fakeSync) Sync(_ context.Context, req out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
	s.req = req
	return out.RemoteSyncResult{
		IncomingPath: s.path, ChecksumSHA256: s.checksum, SizeBytes: s.size,
		SourceHost: "box.example", SourcePath: "remote/data/operational.db", SnapshotCreatedAt: now.Add(-time.Hour),
	}, nil
}

func writeSnapshot(t *testing.T, dir string) *fakeSync {
	t.Helper()
	path := filepath.Join(dir, "incoming.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE snapshot_marker (v TEXT); INSERT INTO snapshot_marker VALUES ('remote')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return &fakeSync{path: path, checksum: hex.EncodeToString(sum[:]), size: int64(len(raw))}
}

func TestSyncSwapsDBAndRecordsResult(t *testing.T) {
	f := newFixture(t)
	snap := writeSnapshot(t, filepath.Dir(f.cfg.SQLite.Path))
	var gotScript, gotKey string
	f.remote = action.Remote{Host: "box.example", KeyPath: "/keys/id", NewSync: func(script, key string) out.RemoteSync {
		gotScript, gotKey = script, key
		return snap
	}}
	old := f.db
	f.mustRun("cycle.start", `{}`)
	if v := f.state(); !v.Available.Sync || v.Disabled["sync"] != "" {
		t.Fatalf("available = %+v disabled = %v", v.Available, v.Disabled)
	}
	f.mustRun("cycle.sync", `{}`)
	if gotScript != action.DefaultSyncScriptPath || gotKey != "/keys/id" {
		t.Errorf("NewSync(%q, %q)", gotScript, gotKey)
	}
	if snap.req.Host != "box.example" || snap.req.LocalDBPath != f.cfg.SQLite.Path ||
		snap.req.RemoteDBHint != f.cfg.Remote.SQLiteRemoteDBHint || snap.req.RetentionDays != f.cfg.Remote.RetentionDays {
		t.Errorf("sync request = %+v", snap.req)
	}
	if len(f.swapped) != 1 {
		t.Fatalf("SwapDB called %d times, want 1", len(f.swapped))
	}
	var marker string
	if err := f.swapped[0].QueryRow(`SELECT v FROM snapshot_marker`).Scan(&marker); err != nil || marker != "remote" {
		t.Fatalf("new handle marker = %q, %v", marker, err)
	}
	if err := old.Ping(); err == nil {
		t.Error("old handle still open, want it closed before the snapshot replaced the file")
	}
	v := f.state()
	s := v.Cycle.Steps["sync"]
	if s.Status != "done" || s.Error != "" {
		t.Fatalf("sync step = %+v", s)
	}
	var r struct {
		Merged            bool      `json:"merged"`
		Migrated          bool      `json:"migrated"`
		SourceHost        string    `json:"source_host"`
		SourcePath        string    `json:"source_path"`
		SnapshotCreatedAt time.Time `json:"snapshot_created_at"`
		SizeBytes         int64     `json:"size_bytes"`
		ChecksumSHA256    string    `json:"checksum_sha256"`
		FKWarnings        []string  `json:"fk_warnings"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Merged || !r.Migrated || r.SourceHost != "box.example" || r.SourcePath != "remote/data/operational.db" ||
		!r.SnapshotCreatedAt.Equal(now.Add(-time.Hour)) || r.SizeBytes != snap.size || r.ChecksumSHA256 != snap.checksum || r.FKWarnings == nil {
		t.Errorf("sync result = %s", s.Result)
	}
	// A second run of a finished step is refused.
	_, err := f.run("cycle.sync", `{}`)
	wantConflict(t, err, "already finished")
}

func TestSyncFailureKeepsAWorkingHandle(t *testing.T) {
	f := newFixture(t)
	f.remote = action.Remote{Host: "box.example", NewSync: func(string, string) out.RemoteSync {
		return &fakeSync{path: filepath.Join(t.TempDir(), "missing.db")}
	}}
	f.mustRun("cycle.start", `{}`)
	if _, err := f.run("cycle.sync", `{}`); err == nil {
		t.Fatal("sync with a missing snapshot succeeded")
	}
	if len(f.swapped) != 1 {
		t.Fatalf("SwapDB called %d times, want 1 (the reopened local DB)", len(f.swapped))
	}
	if err := f.swapped[0].Ping(); err != nil {
		t.Fatalf("reopened handle: %v", err)
	}
	if s := f.state().Cycle.Steps["sync"]; s.Status != "failed" || s.Error == "" {
		t.Fatalf("sync step = %+v, want failed with an error", s)
	}
}

func TestSyncUnavailableWithoutRemote(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	_, err := f.run("cycle.sync", `{}`)
	wantConflict(t, err, "sync is not available on this host")
	v := f.state()
	if v.Available.Sync || v.Available.Telegram {
		t.Errorf("available = %+v, want both false", v.Available)
	}
	if v.Disabled["sync"] != "sync is not available on this host" {
		t.Errorf("disabled = %v", v.Disabled)
	}
	v = decode[view](t, f.mustRun("cycle.next", `{"skip":true}`))
	if s := v.Cycle.Steps["sync"]; s.Status != "skipped" || v.Cycle.Step != "telegram_import" {
		t.Fatalf("after skip: step %s, sync %+v", v.Cycle.Step, s)
	}
}

type fakeTelegram struct {
	err  error
	msgs []telegramimport.Message
	req  out.TelegramImportRequest
}

func (c *fakeTelegram) ExportChannelMessages(_ context.Context, req out.TelegramImportRequest) ([]telegramimport.Message, error) {
	c.req = req
	return c.msgs, c.err
}

func TestTelegramNonInteractiveErrorMessage(t *testing.T) {
	f := newFixture(t)
	f.cfg.Telegram.APIID, f.cfg.Telegram.APIHash, f.cfg.Telegram.Phone = 7, "hash", "+10000"
	f.cfg.Telegram.ChannelID, f.cfg.Telegram.SessionPath = "@channel", filepath.Join(t.TempDir(), "tg.session")
	client := &fakeTelegram{err: errors.New("telegramimport: verification code required but this client is non-interactive (stored session is likely no longer valid)")}
	var gotCfg out.TelegramImportClientConfig
	f.remote = action.Remote{NewTelegramClient: func(c out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
		gotCfg = c
		return client, nil
	}}
	f.mustRun("cycle.start", `{}`)
	f.mustRun("cycle.next", `{"skip":true}`)
	if v := f.state(); !v.Available.Telegram {
		t.Fatal("telegram not available with a client hook")
	}
	_, err := f.run("cycle.telegram_import", `{}`)
	if err == nil || !strings.HasPrefix(err.Error(), "log in to Telegram from the terminal first (") {
		t.Fatalf("err = %v", err)
	}
	if !gotCfg.NonInteractive || gotCfg.SessionPath != f.cfg.Telegram.SessionPath || gotCfg.APIID != 7 || gotCfg.Phone != "+10000" {
		t.Errorf("client config = %+v", gotCfg)
	}
	if client.req.ChannelRef != "@channel" || client.req.Limit != 1000 {
		t.Errorf("request = %+v", client.req)
	}
	s := f.state().Cycle.Steps["telegram_import"]
	if s.Status != "failed" || s.Error != "log in to Telegram from the terminal first (telegramimport: verification code required but this client is non-interactive (stored session is likely no longer valid))" {
		t.Fatalf("step = %+v", s)
	}

	// Client creation failing on auth gets the same message.
	f.remote.NewTelegramClient = func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
		return nil, errors.New("telegram: AUTH_KEY_UNREGISTERED")
	}
	if _, err := f.run("cycle.telegram_import", `{}`); err == nil || !strings.HasPrefix(err.Error(), "log in to Telegram from the terminal first (") {
		t.Fatalf("err = %v", err)
	}

	// Retried after logging in: the import runs and records the counters.
	client.err = nil
	f.remote.NewTelegramClient = func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) { return client, nil }
	f.mustRun("cycle.telegram_import", `{}`)
	s = f.state().Cycle.Steps["telegram_import"]
	if s.Status != "done" || s.Error != "" {
		t.Fatalf("step = %+v", s)
	}
	var counters map[string]int
	if err := json.Unmarshal(s.Result, &counters); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"processed", "imported", "already_known", "unmatched", "skipped", "failed"} {
		if _, ok := counters[k]; !ok {
			t.Errorf("result lacks %s: %s", k, s.Result)
		}
	}
}

func TestTelegramConfigMissingFailsTheStep(t *testing.T) {
	f := newFixture(t)
	f.cfg.Telegram = config.TelegramConfig{}
	f.remote = action.Remote{NewTelegramClient: func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
		t.Fatal("client built without configuration")
		return nil, nil
	}}
	f.mustRun("cycle.start", `{}`)
	f.mustRun("cycle.next", `{"skip":true}`)
	if v := f.state(); !strings.Contains(v.Disabled["telegram_import"], "missing required telegram import configuration") {
		t.Errorf("disabled = %v", v.Disabled)
	}
	if _, err := f.run("cycle.telegram_import", `{}`); err == nil {
		t.Fatal("import without configuration succeeded")
	}
	if s := f.state().Cycle.Steps["telegram_import"]; s.Status != "failed" || !strings.Contains(s.Error, "telegram.api_id") {
		t.Fatalf("step = %+v", s)
	}
}

func TestRosterSkippedWhenEntityStepOff(t *testing.T) {
	f := newFixture(t)
	f.cfg.EntityDiscovery.Enabled = false
	f.mustRun("cycle.start", `{}`)
	f.atStep("roster_sync")
	f.mustRun("cycle.roster_sync", `{}`)
	v := f.state()
	s := v.Cycle.Steps["roster_sync"]
	if s.Status != "skipped" || string(s.Result) != `{"skip_reason":"entity step off or roster disabled"}` {
		t.Fatalf("roster step = %+v (%s)", s, s.Result)
	}
	if v.Disabled["entities"] != "entity_discovery.enabled is false" {
		t.Errorf("disabled = %v", v.Disabled)
	}
	if v := decode[view](t, f.mustRun("cycle.next", `{}`)); v.Cycle.Step != "article_review" {
		t.Fatalf("step = %s", v.Cycle.Step)
	}
}

func seedLabels(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	base := time.Now().UTC().AddDate(0, 0, -25)
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"}
	seed := func(id, title, label string, at time.Time) {
		if err := articles.Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Title: title, FetchedAt: at, PublishedAt: at, Status: article.StatusPendingPublication, Source: src,
		}); err != nil {
			t.Fatal(err)
		}
		if err := labels.Label(ctx, training.Example{ArticleID: id, Label: label, LabeledAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		seed(fmt.Sprintf("p%d", i), fmt.Sprintf("isotopes win big game %d", i), "publish", base.AddDate(0, 0, 2*i))
		seed(fmt.Sprintf("r%d", i), fmt.Sprintf("sponsored shop now huge discount %d", i), "reject", base.AddDate(0, 0, 2*i+1))
	}
}

type trainResult struct {
	Candidates int `json:"candidates"`
	Winner     *struct {
		Version   string  `json:"version"`
		Algorithm string  `json:"algorithm"`
		Precision float64 `json:"precision"`
		Recall    float64 `json:"recall"`
		F1        float64 `json:"f1"`
		PRAUC     float64 `json:"prauc"`
		Dir       string  `json:"dir"`
	} `json:"winner"`
	NoWinner   bool   `json:"no_winner"`
	Promotion  string `json:"promotion"`
	PromotedTo string `json:"promoted_to"`
}

func TestTrainNoWinner(t *testing.T) {
	f := newFixture(t)
	seedLabels(t, f.db)
	f.cfg.EditorialGates.MinPrecision = 1.5 // no candidate can pass
	f.mustRun("cycle.start", `{}`)
	f.atStep("training")
	f.mustRun("cycle.train", `{}`)
	s := f.state().Cycle.Steps["training"]
	if s.Status != "done" {
		t.Fatalf("training = %+v", s)
	}
	var r trainResult
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if !r.NoWinner || r.Winner != nil || r.Candidates == 0 {
		t.Fatalf("result = %s", s.Result)
	}
	_, err := f.run("cycle.promote", `{"promote":true}`)
	wantConflict(t, err, "no winner")
	if v := decode[view](t, f.mustRun("cycle.next", `{}`)); v.Cycle.Step != "summary" {
		t.Fatalf("step = %s", v.Cycle.Step)
	}
}

func TestPromoteOnce(t *testing.T) {
	f := newFixture(t)
	seedLabels(t, f.db)
	f.mustRun("cycle.start", `{}`)
	f.atStep("training")
	f.mustRun("cycle.train", `{}`)
	s := f.state().Cycle.Steps["training"]
	var r trainResult
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.Winner == nil || r.Winner.Dir == "" || r.Winner.Version == "" || r.Winner.Algorithm == "" || s.Status != "pending" {
		t.Fatalf("training = %+v (%s)", s, s.Result)
	}
	_, err := f.run("cycle.next", `{}`)
	wantConflict(t, err, "not finished")
	_, err = f.run("cycle.train", `{}`)
	wantConflict(t, err, "waiting for the promote decision")

	f.mustRun("cycle.promote", `{"promote":true}`)
	if _, err := os.Stat(f.cfg.Model.Path); err != nil {
		t.Fatalf("promoted model: %v", err)
	}
	s = f.state().Cycle.Steps["training"]
	r = trainResult{}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || r.Promotion != "promoted" || r.PromotedTo != f.cfg.Model.Path {
		t.Fatalf("after promote = %+v (%s)", s, s.Result)
	}
	_, err = f.run("cycle.promote", `{"promote":true}`)
	wantConflict(t, err, "already")
	_, err = f.run("cycle.promote", `{"promote":false}`)
	wantConflict(t, err, "already")
}

func TestDeclineRecordsDeclined(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{}`)
	f.setState(func(m map[string]any) {
		m["step"] = "training"
		stepsMap(t, m)["training"] = map[string]any{
			"status": "pending", "result": map[string]any{"candidates": 1, "winner": map[string]any{"version": "v9", "dir": "/nowhere"}, "no_winner": false, "promotion": ""},
		}
	})
	f.mustRun("cycle.promote", `{"promote":false}`)
	s := f.state().Cycle.Steps["training"]
	var r trainResult
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || r.Promotion != "declined" {
		t.Fatalf("after decline = %+v (%s)", s, s.Result)
	}
	if _, err := os.Stat(f.cfg.Model.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("model written on decline: %v", err)
	}
}

func createSession(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if err := sqliteadapter.NewEditorialCycleSessionRepository(db).Create(context.Background(), out.EditorialCycleSession{
		ID: id, StartedAt: now, UpdatedAt: now, Status: status, CurrentStep: "review",
	}); err != nil {
		t.Fatal(err)
	}
}

func sessionStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM editorial_cycle_sessions WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAbandonCancelsSessionAndClearsState(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"paused", "cancelled"},
		{"running", "cancelled"},
		{"completed", "completed"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			f := newFixture(t)
			createSession(t, f.db, "sess-1", tc.status)
			f.mustRun("cycle.start", `{}`)
			f.setState(func(m map[string]any) { m["session_id"] = "sess-1"; m["step"] = "entities" })
			f.mustRun("cycle.abandon", `{}`)
			if got := sessionStatus(t, f.db, "sess-1"); got != tc.want {
				t.Errorf("session status = %s, want %s", got, tc.want)
			}
			if v := f.state(); v.Cycle != nil {
				t.Errorf("cycle = %+v, want null", v.Cycle)
			}
			// A new cycle can start.
			f.mustRun("cycle.start", `{}`)
		})
	}
}

func TestSummaryAndFinish(t *testing.T) {
	f := newFixture(t)
	f.mustRun("cycle.start", `{"entities_verification":"full"}`)
	f.setState(func(m map[string]any) {
		m["step"] = "training"
		m["session_id"] = "sess-9"
		steps := stepsMap(t, m)
		steps["telegram_import"] = map[string]any{"status": "done", "result": map[string]any{"processed": 4, "imported": 2, "already_known": 1, "unmatched": 1, "skipped": 0, "failed": 0}}
		steps["roster_sync"] = map[string]any{"status": "done", "result": map[string]any{"players": 53, "path": "roster.csv"}}
		steps["article_review"] = map[string]any{"status": "done", "result": map[string]any{"articles_reviewed": 5, "publish": 3, "reject": 2}}
		steps["entities"] = map[string]any{"status": "done", "result": map[string]any{"articles": 3, "proposed": 2, "approved": 1, "rejected": 1}}
		steps["event_rules"] = map[string]any{"status": "skipped", "result": map[string]any{"skip_reason": "event rules off"}}
		steps["mined_terms"] = map[string]any{"status": "done", "result": map[string]any{"added": 4}}
		steps["training"] = map[string]any{"status": "done", "result": map[string]any{"candidates": 2, "winner": map[string]any{"version": "v3"}, "promotion": "declined"}}
	})
	v := decode[view](t, f.mustRun("cycle.next", `{}`))
	if v.Cycle.Step != "summary" || v.Cycle.Steps["summary"].Status != "done" {
		t.Fatalf("step = %s summary = %+v", v.Cycle.Step, v.Cycle.Steps["summary"])
	}
	res := f.mustRun("cycle.summary", `{}`)
	sum := decode[struct {
		Report   editorialAudit `json:"report"`
		Markdown string         `json:"markdown"`
		Deploy   string         `json:"deploy"`
	}](t, res)
	want := editorialAudit{
		SessionID: "sess-9", ArticlesReviewed: 5, PublishCount: 3, RejectCount: 2, DictionaryTermsAdded: 4,
		TrainingCandidates: 2, Winner: "v3", WinnerDeclined: true,
		TelegramImportProcessed: 4, TelegramImportImported: 2, TelegramImportAlreadyKnown: 1, TelegramImportUnmatched: 1,
		RosterSync: "synced:53", EntitiesArticles: 3, EntitiesProposed: 2, EntitiesApproved: 1, EntitiesRejected: 1,
		EntitiesVerification: "full", EventRulesSkipped: true, EventRulesSkipReason: "event rules off",
	}
	if sum.Report != want {
		t.Errorf("report = %+v\nwant     %+v", sum.Report, want)
	}
	if sum.Deploy != "run the deploy from the terminal" || !strings.Contains(sum.Markdown, "# Editorial cycle summary — session sess-9") {
		t.Errorf("summary = %+v", sum)
	}
	fin := f.mustRun("cycle.next", `{}`)
	done := decode[struct {
		Cycle       any    `json:"cycle"`
		AuditReport string `json:"audit_report"`
	}](t, fin)
	if done.Cycle != nil || !strings.HasPrefix(done.AuditReport, filepath.Join(filepath.Dir(f.cfg.SQLite.Path), "audit")) {
		t.Fatalf("finish = %+v", done)
	}
	if _, err := os.Stat(done.AuditReport); err != nil {
		t.Fatal(err)
	}
	if v := f.state(); v.Cycle != nil {
		t.Fatal("cycle still present after finishing")
	}
}

// editorialAudit mirrors the comparable fields of editorialcycle.AuditReport.
type editorialAudit struct {
	SessionID                     string
	ArticlesReviewed              int
	PublishCount                  int
	RejectCount                   int
	DictionaryTermsAdded          int
	TrainingCandidates            int
	Winner                        string
	WinnerDeclined                bool
	TelegramImportSkipped         bool
	TelegramImportSkipReason      string
	TelegramImportProcessed       int
	TelegramImportImported        int
	TelegramImportAlreadyKnown    int
	TelegramImportUnmatched       int
	TelegramImportSkippedMessages int
	TelegramImportFailed          int
	RosterSync                    string
	EntitiesSkipped               bool
	EntitiesSkipReason            string
	EntitiesArticles              int
	EntitiesProposed              int
	EntitiesApproved              int
	EntitiesRejected              int
	EntitiesVerification          string
	EntitiesDiscoveryFailed       bool
	EventRulesSkipped             bool
	EventRulesSkipReason          string
	EventRulesSampled             int
	EventRulesProposed            int
	EventRulesApproved            int
	EventRulesApplied             int
	EventRulesDiscoveryFailed     bool
	EventRulesGateBlocked         bool
	EventRulesGateNotExercised    bool
}
