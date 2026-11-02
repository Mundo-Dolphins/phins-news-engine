package event_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

type fixture struct {
	config string
	dbPath string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	return fixture{config: configPath, dbPath: filepath.Join(filepath.Dir(configPath), "operational.db")}
}

// seed stores a claim for articleID in the given state, with one outbox row per
// status in outboxStatuses (destinations telegram then bluesky).
func (f fixture) seed(t *testing.T, key, state, articleID string, outboxStatuses ...string) {
	t.Helper()
	ctx := context.Background()
	db, err := infrasqlite.Open(f.dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	now := time.Now()
	src := source.Source{ID: testsupport.MustID(t, source.TypeRSS, "feed-a"), Name: "Feed A", Type: source.TypeRSS}
	if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/" + articleID, CanonicalURL: "https://example.com/" + articleID,
		Source: src, FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	claims := sqliteadapter.NewEventClaimRepository(db)
	if ok, err := claims.TryClaim(ctx, "injury", key, articleID, json.RawMessage(`{"week":3,"team":"Isotopes"}`), now); err != nil || !ok {
		t.Fatalf("TryClaim = %v, %v", ok, err)
	}
	if state != "claimed" {
		if _, err := db.ExecContext(ctx, `UPDATE event_claims SET state = ? WHERE claim_key = ?`, state, key); err != nil {
			t.Fatalf("set state: %v", err)
		}
	}
	destinations := []string{"telegram", "bluesky"}
	for i, status := range outboxStatuses {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO publication_outbox (article_id, destination, payload, status, available_at, attempt_count, last_error, expires_at)
			VALUES (?, ?, '{}', ?, ?, 6, ?, ?)`,
			articleID, destinations[i], status, now.Add(-time.Hour), "boom "+status, now.Add(-time.Minute)); err != nil {
			t.Fatalf("seed outbox: %v", err)
		}
	}
}

func (f fixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return testsupport.Execute(t, append([]string{"--config", f.config, "event", "claims"}, args...)...)
}

func (f fixture) claimState(t *testing.T, key string) string {
	t.Helper()
	db, err := infrasqlite.Open(f.dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var s string
	if err := db.QueryRow(`SELECT state FROM event_claims WHERE claim_key = ?`, key).Scan(&s); err != nil {
		return "absent"
	}
	return s
}

func (f fixture) outboxRow(t *testing.T, articleID, destination string) (status string, attempts int, expiresNull bool) {
	t.Helper()
	db, err := infrasqlite.Open(f.dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var exp *string
	if err := db.QueryRow(`SELECT status, attempt_count, expires_at FROM publication_outbox WHERE article_id = ? AND destination = ?`,
		articleID, destination).Scan(&status, &attempts, &exp); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	return status, attempts, exp == nil
}

func TestList_FiltersAndShowsOutboxAndError(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "2026-11-01", "failed", "a1", "dead")
	f.seed(t, "2026-11-02", "published", "a2", "delivered")

	out, err := f.run(t, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"key=2026-11-01", "state=failed", "outbox=telegram:dead", "last_error=boom dead", "key=2026-11-02", "state=published", "2 claim(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	out, _ = f.run(t, "list", "--state", "failed")
	if strings.Contains(out, "2026-11-02") || !strings.Contains(out, "1 claim(s)") {
		t.Errorf("--state failed output = %s", out)
	}
	out, _ = f.run(t, "list", "--handler", "other")
	if !strings.Contains(out, "0 claim(s)") {
		t.Errorf("--handler other output = %s", out)
	}
	out, _ = f.run(t, "list", "--since", "1h")
	if !strings.Contains(out, "2 claim(s)") {
		t.Errorf("--since 1h output = %s", out)
	}
	out, _ = f.run(t, "list", "--since", "2999-01-01")
	if !strings.Contains(out, "0 claim(s)") {
		t.Errorf("--since future output = %s", out)
	}
	if _, err := f.run(t, "list", "--state", "bogus"); err == nil {
		t.Error("unknown --state: want error")
	}
	if _, err := f.run(t, "list", "--since", "yesterday"); err == nil {
		t.Error("bad --since: want error")
	}
}

func TestShow_PrintsFullJSON(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "k1", "failed", "a1", "dead")
	out, err := f.run(t, "show", "injury", "k1")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	var got struct {
		State  string          `json:"state"`
		Data   json.RawMessage `json:"data"`
		Outbox []struct {
			Destination, Status string
		} `json:"outbox"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("show output is not JSON: %v\n%s", err, out)
	}
	if got.State != "failed" || !strings.Contains(string(got.Data), "Isotopes") || len(got.Outbox) != 1 || got.Outbox[0].Status != "dead" {
		t.Errorf("show = %+v", got)
	}
	if _, err := f.run(t, "show", "injury", "missing"); err == nil {
		t.Error("show missing: want error")
	}
}

func TestRetry_FailedClaimRequeuesDeadRows(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "k1", "failed", "a1", "delivered", "dead")

	out, err := f.run(t, "retry", "injury", "k1", "--dry-run")
	if err != nil || !strings.Contains(out, "would re-enqueue 1") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if got := f.claimState(t, "k1"); got != "failed" {
		t.Fatalf("dry run changed the claim to %s", got)
	}

	if out, err = f.run(t, "retry", "injury", "k1"); err != nil {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	if got := f.claimState(t, "k1"); got != "claimed" {
		t.Errorf("claim state = %s, want claimed", got)
	}
	status, attempts, expiresNull := f.outboxRow(t, "a1", "bluesky")
	if status != "pending" || attempts != 0 || !expiresNull {
		t.Errorf("bluesky row = %s attempts=%d expiresNull=%v, want pending 0 true", status, attempts, expiresNull)
	}
	if status, _, _ := f.outboxRow(t, "a1", "telegram"); status != "delivered" {
		t.Errorf("delivered telegram row changed to %s", status)
	}
}

func TestRetry_Refusals(t *testing.T) {
	tests := []struct {
		name   string
		state  string
		rows   []string
		want   string
		remain string
	}{
		{"published", "published", []string{"delivered"}, "not failed", "published"},
		{"claimed", "claimed", []string{"pending"}, "not failed", "claimed"},
		{"pending row", "failed", []string{"dead", "pending"}, "in-flight", "failed"},
		{"leased row", "failed", []string{"leased"}, "in-flight", "failed"},
		{"retryable row", "failed", []string{"retryable"}, "in-flight", "failed"},
		{"quarantined row", "failed", []string{"quarantined"}, "in-flight", "failed"},
		{"sent unconfirmed row", "failed", []string{"sent_unconfirmed"}, "in-flight", "failed"},
		{"no dead row", "failed", []string{"delivered"}, "no dead outbox row", "failed"},
		{"no outbox row", "failed", nil, "no dead outbox row", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(t, "k1", tt.state, "a1", tt.rows...)
			for _, args := range [][]string{{"retry", "injury", "k1"}, {"retry", "injury", "k1", "--dry-run"}} {
				_, err := f.run(t, args...)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("%v error = %v, want it to contain %q", args, err, tt.want)
				}
			}
			if got := f.claimState(t, "k1"); got != tt.remain {
				t.Errorf("claim state after refusal = %s, want %s", got, tt.remain)
			}
		})
	}
	f := newFixture(t)
	if _, err := f.run(t, "retry", "injury", "nope"); err == nil || !strings.Contains(err.Error(), "no claim") {
		t.Errorf("retry missing error = %v", err)
	}
}

func TestRelease(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "failed-key", "failed", "a1", "dead")
	f.seed(t, "claimed-key", "claimed", "a2")
	f.seed(t, "published-key", "published", "a3", "delivered")

	if _, err := f.run(t, "release", "injury", "failed-key"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("release without --yes error = %v", err)
	}
	if got := f.claimState(t, "failed-key"); got != "failed" {
		t.Errorf("claim without --yes is %s, want untouched", got)
	}

	out, err := f.run(t, "release", "injury", "failed-key", "--yes")
	if err != nil || !strings.Contains(out, "released injury/failed-key (by ") {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	if got := f.claimState(t, "failed-key"); got != "absent" {
		t.Errorf("released claim is %s, want absent", got)
	}
	if _, err := f.run(t, "release", "injury", "claimed-key", "--yes"); err != nil {
		t.Errorf("release claimed: %v", err)
	}
	if _, err := f.run(t, "release", "injury", "published-key", "--yes"); err == nil || !strings.Contains(err.Error(), "published") {
		t.Errorf("release published error = %v", err)
	}
	if got := f.claimState(t, "published-key"); got != "published" {
		t.Errorf("published claim is %s, want untouched", got)
	}
	if _, err := f.run(t, "release", "injury", "missing", "--yes"); err == nil || !strings.Contains(err.Error(), "no claim") {
		t.Errorf("release missing error = %v", err)
	}
}
