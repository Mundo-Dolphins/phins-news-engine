package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestOutboxRepository_SupersedesIsotopes49ersLiveQueueOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	state := func(id, phase, matchID string, status article.Status, lane publishing.PublicationLane) {
		a := article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: mustID(t, source.TypeRSS, "supersession-"+id), Type: source.TypeRSS},
			FetchedAt: now, PublishedAt: now, Status: status,
			Metadata: map[string]string{"match_id": matchID, "match_home_team": "SPR", "match_away_team": "HC", "match_phase": phase, "match_state_kind": "live"},
		}
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		if status == article.StatusPendingPublication {
			if err := outbox.EnqueueLane(ctx, id, "{}", lane, now); err != nil {
				t.Fatalf("enqueue %s: %v", id, err)
			}
		}
	}

	state("kickoff", "kickoff", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	state("touchdown", "q1", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	state("other-game", "q1", "spr-jxv-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	state("analysis", "final", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	// Same match/phase as the live-lane items above, but this one landed in
	// the normal lane (its source isn't allow-listed) — it must NOT be
	// discarded just because it happens to carry in-game metadata.
	state("normal-lane-ingame", "q1", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneNormal)
	// Postgame analysis carries the same match identity but is explicitly not
	// a live-state publication and must survive the final.
	if _, err := db.ExecContext(ctx, `UPDATE articles SET metadata = json_set(metadata, '$.match_state_kind', 'postgame') WHERE id = 'analysis'`); err != nil {
		t.Fatal(err)
	}
	state("final", "final", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	items, err := outbox.SupersedeClaimableMatch(ctx, "spr-hc-2026-w3", "final", now)
	if err != nil {
		t.Fatalf("SupersedeClaimableMatch() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("superseded = %+v, want kickoff and touchdown", items)
	}

	// A later live item can be claimed concurrently with a repeated terminal
	// observation; the active lease is deliberately left to the publisher.
	state("race", "q2", "spr-hc-2026-w3", article.StatusPendingPublication, publishing.LaneLive)
	if _, err := db.ExecContext(ctx, `UPDATE publication_outbox SET status='leased', claimed_at=?, claimed_by='publisher-1' WHERE article_id='race'`, now); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id     string
		status article.Status
		outbox string
	}{
		{"kickoff", article.StatusDiscarded, "dead"},
		{"touchdown", article.StatusDiscarded, "dead"},
		{"other-game", article.StatusPendingPublication, "pending"},
		{"analysis", article.StatusPendingPublication, "pending"},
		{"normal-lane-ingame", article.StatusPendingPublication, "pending"},
	} {
		var gotStatus, gotOutbox string
		if err := db.QueryRowContext(ctx, `SELECT status FROM articles WHERE id = ?`, tc.id).Scan(&gotStatus); err != nil {
			t.Fatal(err)
		}
		if gotStatus != string(tc.status) {
			t.Errorf("article %s status = %s, want %s", tc.id, gotStatus, tc.status)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM publication_outbox WHERE article_id = ?`, tc.id).Scan(&gotOutbox); err != nil {
			t.Fatal(err)
		}
		if gotOutbox != tc.outbox {
			t.Errorf("outbox %s status = %s, want %s", tc.id, gotOutbox, tc.outbox)
		}
	}
	var raceOutboxStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM publication_outbox WHERE article_id = 'race'`).Scan(&raceOutboxStatus); err != nil {
		t.Fatal(err)
	}
	if raceOutboxStatus != "leased" {
		t.Fatalf("race outbox status = %s, want leased", raceOutboxStatus)
	}

	items, err = outbox.SupersedeClaimableMatch(ctx, "spr-hc-2026-w3", "final", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("second supersession = %+v, want empty (idempotent)", items)
	}
}
