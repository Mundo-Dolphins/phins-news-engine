package outbox_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/outbox"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t   *testing.T
	db  *sql.DB
	cfg *config.Config
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return fixture{t: t, db: db, cfg: cfg, log: &bytes.Buffer{}}
}

// seed stores an article published at publishedAt with one telegram outbox
// row in the given status, and returns the row id.
func (f fixture) seed(articleID, status string, publishedAt time.Time) int64 {
	f.t.Helper()
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(f.t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
		Source: src, FetchedAt: publishedAt, PublishedAt: publishedAt, Status: article.StatusPublishing,
	}); err != nil {
		f.t.Fatalf("seed article: %v", err)
	}
	res, err := f.db.ExecContext(ctx, `
		INSERT INTO publication_outbox (article_id, destination, payload, status, available_at, attempt_count, sent_message_id, ambiguous_reason, sent_at, created_at)
		VALUES (?, 'telegram', '{}', ?, ?, 2, 'm1', 'timeout', ?, ?)`,
		articleID, status, publishedAt, publishedAt, publishedAt)
	if err != nil {
		f.t.Fatalf("seed outbox: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f fixture) status(id int64) string {
	f.t.Helper()
	var s string
	if err := f.db.QueryRow(`SELECT status FROM publication_outbox WHERE id = ?`, id).Scan(&s); err != nil {
		f.t.Fatalf("status: %v", err)
	}
	return s
}

func (f fixture) run(name, params string) (action.Result, error) {
	f.t.Helper()
	var a action.Action
	for _, c := range outbox.Actions() {
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
	env := action.Env{DB: f.db, Config: f.cfg, Mode: action.ModeProduction, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(f.log, nil)), Actor: "user:ana"}
	return a.Run(context.Background(), env, p)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestMetadata(t *testing.T) {
	want := map[string]action.Kind{"outbox.status": action.KindRead, "outbox.backlog": action.KindRead,
		"outbox.inspect": action.KindRead, "outbox.expire_preview": action.KindRead,
		"outbox.expire": action.KindDestructive, "outbox.repair": action.KindDestructive}
	as := outbox.Actions()
	if len(as) != len(want) {
		t.Fatalf("%d actions", len(as))
	}
	for _, a := range as {
		if a.Group != "outbox" || a.Kind != want[a.Name] || len(a.Modes) != 2 || a.Title == "" || a.Description == "" {
			t.Errorf("%+v", a)
		}
	}
	var reg action.Registry
	reg.Add(as...)
}

func TestStatusListsEveryStatus(t *testing.T) {
	f := newFixture(t)
	f.seed("a1", "pending", now.Add(-time.Hour))
	res, err := f.run("outbox.status", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"pending", "leased", "sent_unconfirmed", "retryable", "quarantined", "delivered", "dead"}
	if res.Render != "table" || len(res.Rows) != len(order) || len(res.Columns) != 4 {
		t.Fatalf("res = %+v", res)
	}
	for i, st := range order {
		if res.Rows[i]["status"] != st {
			t.Errorf("row %d = %v, want %s", i, res.Rows[i]["status"], st)
		}
	}
	if res.Rows[0]["count"] != 1 || res.Rows[1]["count"] != 0 {
		t.Errorf("counts = %v %v", res.Rows[0], res.Rows[1])
	}
}

func TestBacklogCriticalIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.seed("a1", "pending", now.Add(-time.Hour))
	f.cfg.Publication.Backlog.PendingWarning = 1
	f.cfg.Publication.Backlog.PendingCritical = 1
	f.cfg.Publication.Backlog.AgeWarningMinutes = 1
	f.cfg.Publication.Backlog.AgeCriticalMinutes = 1
	res, err := f.run("outbox.backlog", `{}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Render != "object" || !strings.HasPrefix(res.Summary, "level=critical pending=") {
		t.Fatalf("res = %+v", res)
	}
}

func TestBacklogBadWindow(t *testing.T) {
	f := newFixture(t)
	_, err := f.run("outbox.backlog", `{"window":"soon"}`)
	wantCode(t, err, action.CodeInvalidParams)
	_, err = f.run("outbox.backlog", `{"window":"0s"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestInspectRejectsOtherStatus(t *testing.T) {
	_, err := newFixture(t).run("outbox.inspect", `{"status":"pending"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestInspectQuarantined(t *testing.T) {
	f := newFixture(t)
	id := f.seed("a1", "quarantined", now.Add(-time.Hour))
	res, err := f.run("outbox.inspect", `{}`)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	r := res.Rows[0]
	if r["id"] != id || r["article_id"] != "a1" || r["status"] != "quarantined" || r["attempts"] != 2 ||
		r["sent_message_id"] != "m1" || r["ambiguous_reason"] != "timeout" {
		t.Errorf("row = %v", r)
	}
}

func TestExpirePreviewDoesNotWrite(t *testing.T) {
	f := newFixture(t)
	id := f.seed("a1", "pending", now.Add(-48*time.Hour))
	res, err := f.run("outbox.expire_preview", `{"older_than":"24h"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Summary != "1 candidate(s), cutoff "+now.Add(-24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("res = %+v", res)
	}
	if got := f.status(id); got != "pending" {
		t.Errorf("status = %s", got)
	}
}

func TestExpireAppliesPreview(t *testing.T) {
	f := newFixture(t)
	id := f.seed("a1", "pending", now.Add(-48*time.Hour))
	res, err := f.run("outbox.expire", `{"older_than":"24h","reason":"stale"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Render != "text" || res.Text != "applied=1 reason_code=outbox_expired_operator" {
		t.Fatalf("res = %+v", res)
	}
	if got := f.status(id); got == "pending" {
		t.Errorf("status still %s", got)
	}
}

func TestExpireRequiresReason(t *testing.T) {
	_, err := newFixture(t).run("outbox.expire", `{"older_than":"24h"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestRepairArticleMismatchConflicts(t *testing.T) {
	f := newFixture(t)
	id := f.seed("A", "quarantined", now.Add(-time.Hour))
	f.seed("B", "pending", now.Add(-time.Hour))
	_, err := f.run("outbox.repair", `{"id":`+itoa(id)+`,"action":"retry","article_id":"B","reason":"x"}`)
	wantCode(t, err, action.CodeConflict)
	if got := f.status(id); got != "quarantined" {
		t.Errorf("status = %s", got)
	}
}

func TestRepairWrongStatusConflicts(t *testing.T) {
	f := newFixture(t)
	id := f.seed("A", "pending", now.Add(-time.Hour))
	_, err := f.run("outbox.repair", `{"id":`+itoa(id)+`,"action":"retry","article_id":"A","reason":"x"}`)
	wantCode(t, err, action.CodeConflict)
}

func TestRepairRetry(t *testing.T) {
	f := newFixture(t)
	id := f.seed("A", "quarantined", now.Add(-time.Hour))
	res, err := f.run("outbox.repair", `{"id":`+itoa(id)+`,"action":"retry","article_id":"A","reason":"checked"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "repaired outbox item "+itoa(id)+": action=retry" {
		t.Errorf("text = %q", res.Text)
	}
	if got := f.status(id); got != "retryable" {
		t.Errorf("status = %s", got)
	}
}

func TestRepairUnknownID(t *testing.T) {
	f := newFixture(t)
	f.seed("A", "quarantined", now.Add(-time.Hour))
	_, err := f.run("outbox.repair", `{"id":9999,"action":"retry","article_id":"A","reason":"x"}`)
	wantCode(t, err, action.CodeNotFound)
	_, err = f.run("outbox.repair", `{"id":1,"action":"retry","article_id":"nope","reason":"x"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestBadDuration(t *testing.T) {
	f := newFixture(t)
	for name, params := range map[string]string{
		"outbox.expire_preview": `{"older_than":"24"}`,
		"outbox.expire":         `{"older_than":"24","reason":"r"}`,
	} {
		_, err := f.run(name, params)
		wantCode(t, err, action.CodeInvalidParams)
		if !strings.Contains(err.Error(), "older_than") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

func TestInspectRejectsNonPositiveLimit(t *testing.T) {
	_, err := newFixture(t).run("outbox.inspect", `{"status":"quarantined","limit":0}`)
	wantCode(t, err, action.CodeInvalidParams)
}
