package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestOutboxRepository_EnqueueDestination_OneRowPerArticleAndDestination(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, d := range []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky, publishing.DestinationBluesky} {
		if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, d, past); err != nil {
			t.Fatalf("EnqueueDestination(%s): %v", d, err)
		}
	}
	if err := repo.Enqueue(ctx, "a1", "{}", past); err != nil { // legacy path == telegram, idempotent
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE article_id='a1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d, %v; want 2", n, err)
	}
	if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, "mastodon", past); err == nil {
		t.Fatal("unknown destination accepted")
	}
}

func TestOutboxRepository_ClaimBatch_ReturnsDestinationAndRowsAreIndependent(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	now := past.Add(time.Hour)
	for _, d := range []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky} {
		if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, d, past); err != nil {
			t.Fatal(err)
		}
	}

	items, err := repo.ClaimBatch(ctx, "w", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	byDest := map[publishing.Destination]appout.OutboxItem{}
	for _, it := range items {
		byDest[it.Destination] = it
	}
	tg, bsky := byDest[publishing.DestinationTelegram], byDest[publishing.DestinationBluesky]
	if len(items) != 2 || tg.ID == 0 || bsky.ID == 0 || tg.ID == bsky.ID {
		t.Fatalf("claimed %+v, want one row per destination", items)
	}

	// Failing the bluesky row leaves the telegram row untouched, and vice versa.
	if err := repo.MarkRetryable(ctx, bsky.ID, "boom", now.Add(time.Hour), true, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkDelivered(ctx, tg.ID, now); err != nil {
		t.Fatal(err)
	}
	status := func(id int64) string {
		var s string
		if err := db.QueryRowContext(ctx, `SELECT status FROM publication_outbox WHERE id=?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if status(tg.ID) != "delivered" || status(bsky.ID) != "dead" {
		t.Fatalf("statuses telegram=%s bluesky=%s", status(tg.ID), status(bsky.ID))
	}
}

func TestOutboxRepository_TelegramCountersAndStatusIgnoreOtherDestinations(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	now := past.Add(time.Hour)
	for _, d := range []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky} {
		if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, d, past); err != nil {
			t.Fatal(err)
		}
	}
	items, err := repo.ClaimBatch(ctx, "w", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if err := repo.RecordAttempt(ctx, appout.Attempt{OutboxID: it.ID, ArticleID: "a1", AttemptedAt: now, Outcome: publishing.AttemptSucceeded}); err != nil {
			t.Fatal(err)
		}
	}
	since := now.Add(-time.Minute)
	if n, err := repo.CountSucceededSince(ctx, since); err != nil || n != 1 {
		t.Fatalf("CountSucceededSince = %d, %v; want 1 (telegram only)", n, err)
	}
	if n, err := repo.CountSucceededSinceLane(ctx, publishing.LaneNormal, since); err != nil || n != 1 {
		t.Fatalf("CountSucceededSinceLane = %d, %v; want 1", n, err)
	}
	counts, err := repo.StatusCounts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if counts[publishing.OutboxLeased].Count != 1 {
		t.Fatalf("StatusCounts leased = %d, want the telegram row only", counts[publishing.OutboxLeased].Count)
	}
}

func TestOutboxRepository_OpenDestinationsAndDestinationOf(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	if got, err := repo.OpenDestinations(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty outbox OpenDestinations = %v, %v", got, err)
	}
	if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, publishing.DestinationBluesky, past); err != nil {
		t.Fatal(err)
	}
	got, err := repo.OpenDestinations(ctx)
	if err != nil || len(got) != 1 || got[0] != publishing.DestinationBluesky {
		t.Fatalf("OpenDestinations = %v, %v; want [bluesky]", got, err)
	}
	if d, err := repo.DestinationOf(ctx, mustOutboxID(t, db, "a1")); err != nil || d != publishing.DestinationBluesky {
		t.Fatalf("DestinationOf = %q, %v", d, err)
	}
}

func TestOutboxRepository_CountSucceededPerDestination(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	now := past.Add(time.Hour)
	for _, d := range []publishing.Destination{publishing.DestinationTelegram, publishing.DestinationBluesky} {
		if err := repo.EnqueueDestination(ctx, "a1", "{}", publishing.LaneNormal, d, past); err != nil {
			t.Fatal(err)
		}
	}
	items, err := repo.ClaimBatch(ctx, "w", 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		// two successes on bluesky, one on telegram
		n := 1
		if it.Destination == publishing.DestinationBluesky {
			n = 2
		}
		for i := 0; i < n; i++ {
			if err := repo.RecordAttempt(ctx, appout.Attempt{OutboxID: it.ID, ArticleID: "a1", AttemptedAt: now, Outcome: publishing.AttemptSucceeded}); err != nil {
				t.Fatal(err)
			}
		}
	}
	since := now.Add(-time.Minute)
	for d, want := range map[publishing.Destination]int{publishing.DestinationTelegram: 1, publishing.DestinationBluesky: 2} {
		if n, err := repo.CountSucceededSinceDestination(ctx, d, publishing.LaneNormal, since); err != nil || n != want {
			t.Errorf("CountSucceededSinceDestination(%s) = %d, %v; want %d", d, n, err, want)
		}
		if n, err := repo.CountSucceededByPrimaryEntitySinceDestination(ctx, d, publishing.LaneNormal, "", since); err != nil || n != want {
			t.Errorf("CountSucceededByPrimaryEntitySinceDestination(%s) = %d, %v; want %d", d, n, err, want)
		}
	}
}

func TestOutboxRepository_EnqueueDestination_ValidatesAndPersistsMessagePayload(t *testing.T) {
	db := newTestDB(t)
	mustSaveArticle(t, sqliteadapter.NewArticleRepository(db), "a1")
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, bad := range []string{
		`{"text":"hi","images":[{"path":"","mime":"image/png"}]}`,
		`{"text":"hi","images":[{"path":"/abs.png","mime":"image/png"}]}`,
		`{"text":"hi","images":[{"path":"../x.png","mime":"image/png"}]}`,
		"not json",
	} {
		if err := repo.EnqueueDestination(ctx, "a1", bad, publishing.LaneNormal, publishing.DestinationBluesky, past); err == nil {
			t.Errorf("payload %q accepted", bad)
		}
	}

	want := publishing.MessagePayload{Text: "hi", Images: []publishing.ImageRef{{Path: "w8.png", MIME: "image/png", Alt: "alt"}}}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.EnqueueDestination(ctx, "a1", raw, publishing.LaneNormal, publishing.DestinationBluesky, past); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ClaimBatch(ctx, "w", 10, time.Minute, past.Add(time.Hour))
	if err != nil || len(items) != 1 {
		t.Fatalf("claimed %v, %v; want 1 item", items, err)
	}
	got, err := publishing.ParseMessagePayload(items[0].Payload)
	if err != nil || got.Text != want.Text || len(got.Images) != 1 || got.Images[0] != want.Images[0] {
		t.Fatalf("stored payload = %+v, %v; want %+v", got, err, want)
	}
}
