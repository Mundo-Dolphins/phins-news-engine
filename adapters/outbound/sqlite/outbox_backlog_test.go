package sqlite_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

type backlogRow struct {
	id, status, lane, lastError string
	publishedAgo, createdAgo    time.Duration
	sentAgo, updatedAgo         time.Duration // zero = unset (updated defaults to created)
}

func seedBacklogRow(t *testing.T, db *sql.DB, now time.Time, r backlogRow) {
	t.Helper()
	mustSaveArticleWithPublishedAt(t, sqliteadapter.NewArticleRepository(db), r.id, now.Add(-r.publishedAgo))
	// Mirror production: created_at/updated_at default to CURRENT_TIMESTAMP
	// ("2006-01-02 15:04:05"); sent_at and explicit updates are RFC 3339.
	rfc := func(ago time.Duration) any {
		if ago == 0 {
			return nil
		}
		return now.Add(-ago).UTC().Format(time.RFC3339Nano)
	}
	dflt := func(ago time.Duration) any { return now.Add(-ago).UTC().Format("2006-01-02 15:04:05") }
	updated := dflt(r.createdAgo)
	if r.updatedAgo != 0 {
		updated = now.Add(-r.updatedAgo).UTC().Format(time.RFC3339Nano)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO publication_outbox (article_id, status, lane, available_at, created_at, updated_at, sent_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.id, r.status, r.lane, dflt(r.createdAgo), dflt(r.createdAgo), updated, rfc(r.sentAgo), r.lastError); err != nil {
		t.Fatalf("seed outbox row %s: %v", r.id, err)
	}
}

func seedBacklogEvent(t *testing.T, db *sql.DB, now time.Time, articleID, eventType, reason string, ago time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO article_processing_events (article_id, occurred_at, to_status, event_type, reason_code)
		VALUES (?, ?, 'x', ?, ?)`, articleID, now.Add(-ago).UTC().Format(time.RFC3339Nano), eventType, reason); err != nil {
		t.Fatalf("seed event: %v", err)
	}
}

func TestOutboxRepository_BacklogSnapshot(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	m := time.Minute
	for _, r := range []backlogRow{
		{id: "p1", status: "pending", lane: "normal", publishedAgo: 4 * time.Hour, createdAgo: 3 * time.Hour},
		{id: "p2", status: "pending", lane: "live", publishedAgo: 20 * m, createdAgo: 10 * m},
		{id: "r1", status: "retryable", lane: "normal", publishedAgo: 40 * m, createdAgo: 30 * m},
		{id: "l1", status: "leased", lane: "normal", publishedAgo: 3 * time.Hour, createdAgo: 2 * time.Hour},
		{id: "d1", status: "delivered", lane: "normal", publishedAgo: 100 * m, createdAgo: 90 * m, sentAgo: 40 * m},
		{id: "d2", status: "delivered", lane: "normal", publishedAgo: 60 * m, createdAgo: 50 * m, sentAgo: 30 * m},
		{id: "d3", status: "delivered", lane: "normal", publishedAgo: 50 * m, createdAgo: 40 * m, sentAgo: 20 * m},
		{id: "x1", status: "dead", lane: "normal", publishedAgo: 5 * time.Hour, createdAgo: 4 * time.Hour, updatedAgo: 5 * m, lastError: "superseded_by_final:z"},
		{id: "x2", status: "dead", lane: "normal", publishedAgo: 5 * time.Hour, createdAgo: 4 * time.Hour, updatedAgo: 5 * time.Hour, lastError: "superseded_by_final:z"},
		{id: "x3", status: "dead", lane: "normal", publishedAgo: 5 * time.Hour, createdAgo: 4 * time.Hour, updatedAgo: 5 * m, lastError: "telegram: chat not found"},
	} {
		seedBacklogRow(t, db, now, r)
	}
	seedBacklogEvent(t, db, now, "p1", "outbox_expired", "publish_expired", 15*m)
	seedBacklogEvent(t, db, now, "p1", "outbox_expired", "outbox_expired_operator", 14*m)
	seedBacklogEvent(t, db, now, "p1", "outbox_expired", "publish_expired", 3*time.Hour) // outside window
	seedBacklogEvent(t, db, now, "p1", "publish_deferred", "burst_limit", 5*m)
	seedBacklogEvent(t, db, now, "p2", "publish_deferred", "burst_limit", 6*m)
	seedBacklogEvent(t, db, now, "p2", "publish_deferred", "telegram_rate_limit", 7*m)
	seedBacklogEvent(t, db, now, "p2", "publish_deferred", "burst_limit", 3*time.Hour) // outside window

	got, err := sqliteadapter.NewOutboxRepository(db).BacklogSnapshot(context.Background(), now, time.Hour)
	if err != nil {
		t.Fatalf("BacklogSnapshot() error = %v", err)
	}

	wantStatuses := []publishing.BacklogStatusCount{
		{Status: "leased", Lane: "normal", Count: 1},
		{Status: "pending", Lane: "live", Count: 1},
		{Status: "pending", Lane: "normal", Count: 1},
		{Status: "retryable", Lane: "normal", Count: 1},
	}
	if !reflect.DeepEqual(got.Statuses, wantStatuses) {
		t.Errorf("Statuses = %+v, want %+v", got.Statuses, wantStatuses)
	}
	if got.BacklogCount != 3 {
		t.Errorf("BacklogCount = %d, want 3 (pending+retryable)", got.BacklogCount)
	}
	if got.OldestPendingAgeSeconds != 3*3600 {
		t.Errorf("OldestPendingAgeSeconds = %d, want 10800 (from created_at, not published_at)", got.OldestPendingAgeSeconds)
	}
	// Lags: d1 60m, d2 30m, d3 30m (all sent inside the window).
	wantLag := publishing.LagStats{Samples: 3, P50Seconds: 30 * 60, P95Seconds: 60 * 60, MaxSeconds: 60 * 60}
	if got.Lag != wantLag {
		t.Errorf("Lag = %+v, want %+v (sent_at - published_at)", got.Lag, wantLag)
	}
	if got.Enqueued != 4 { // created inside the window: p2, r1, d3, d2
		t.Errorf("Enqueued = %d, want 4", got.Enqueued)
	}
	if got.Delivered != 3 || got.Expired != 2 || got.Superseded != 1 {
		t.Errorf("Delivered/Expired/Superseded = %d/%d/%d, want 3/2/1", got.Delivered, got.Expired, got.Superseded)
	}
	wantDeferrals := []publishing.ReasonCount{{Reason: "burst_limit", Count: 2}, {Reason: "telegram_rate_limit", Count: 1}}
	if !reflect.DeepEqual(got.Deferrals, wantDeferrals) {
		t.Errorf("Deferrals = %+v, want %+v", got.Deferrals, wantDeferrals)
	}
	if got.WindowSeconds != 3600 || !got.GeneratedAt.Equal(now) {
		t.Errorf("window/generated = %d/%v", got.WindowSeconds, got.GeneratedAt)
	}
}

func TestOutboxRepository_BacklogSnapshot_EmptyDatabase(t *testing.T) {
	db := newTestDB(t)
	got, err := sqliteadapter.NewOutboxRepository(db).BacklogSnapshot(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("BacklogSnapshot() error = %v", err)
	}
	if got.BacklogCount != 0 || got.OldestPendingAgeSeconds != 0 || got.Lag != (publishing.LagStats{}) || len(got.Statuses) != 0 || len(got.Deferrals) != 0 {
		t.Errorf("empty DB snapshot = %+v, want all zero", got)
	}
}

func TestOutboxRepository_BacklogSnapshot_UsesIndexes(t *testing.T) {
	db := newTestDB(t)
	for _, q := range []string{
		`SELECT COUNT(1) FROM publication_outbox WHERE created_at >= 'x'`,
		`SELECT created_at FROM publication_outbox WHERE status IN ('pending','retryable') ORDER BY created_at LIMIT 1`,
		`SELECT COUNT(1) FROM publication_outbox WHERE status='dead' AND updated_at >= 'x' AND last_error LIKE 'superseded_by_final:%'`,
	} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			plan += d + "\n"
		}
		_ = rows.Close()
		if !strings.Contains(plan, "USING") || strings.Contains(plan, "SCAN publication_outbox\n") {
			t.Errorf("query %q is not index-backed: %s", q, plan)
		}
	}
}
