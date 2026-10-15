package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestDictionaryHistoryRepository_RecordThenGet(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)

	rec := out.DictionaryHistoryRecord{
		Version: "1.0.1", ContentHash: "hash1", CreatedAt: time.Now().UTC(),
		ChangeCount: 3, SourceSnapshotRef: "fetched-x.json", OperationKind: "update", PredecessorVersion: "1.0.0",
	}
	if err := repo.Record(context.Background(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := repo.Get(context.Background(), "1.0.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContentHash != "hash1" || got.ChangeCount != 3 || got.OperationKind != "update" {
		t.Fatalf("got %+v", got)
	}
}

func TestDictionaryHistoryRepository_ListReturnsAllVersionsNewestFirst(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	base := time.Now().UTC()
	_ = repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.0", ContentHash: "h0", CreatedAt: base})
	_ = repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.1", ContentHash: "h1", CreatedAt: base.Add(time.Minute)})

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Version != "1.0.1" || list[1].Version != "1.0.0" {
		t.Fatalf("expected newest-first order, got %+v", list)
	}
}

func TestDictionaryHistoryRepository_SetPromoted(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	_ = repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.0", ContentHash: "h0"})
	if err := repo.SetPromoted(ctx, "1.0.0"); err != nil {
		t.Fatalf("SetPromoted: %v", err)
	}
	got, err := repo.Get(ctx, "1.0.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Promoted {
		t.Fatal("expected Promoted = true after SetPromoted")
	}
}

func TestDictionaryHistoryRepository_ReRecordDoesNotResetPromotedOrCreatedAt(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	originalCreatedAt := time.Now().UTC().Add(-time.Hour)
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: "h0", CreatedAt: originalCreatedAt,
	}); err != nil {
		t.Fatalf("initial Record: %v", err)
	}
	if err := repo.SetPromoted(ctx, "1.0.0"); err != nil {
		t.Fatalf("SetPromoted: %v", err)
	}

	// Simulate a retry/duplicate event: Record is called again for the same
	// version with a fresh struct whose Promoted field is the Go zero value
	// (false) and whose CreatedAt is the current call time, not the
	// original. Neither should clobber what's already stored.
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: "h0-updated", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("re-Record: %v", err)
	}

	got, err := repo.Get(ctx, "1.0.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Promoted {
		t.Fatal("expected Promoted to remain true after re-Record, got false")
	}
	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Fatalf("expected CreatedAt to remain %v after re-Record, got %v", originalCreatedAt, got.CreatedAt)
	}
	if got.ContentHash != "h0-updated" {
		t.Fatalf("expected ContentHash to be updated by re-Record, got %q", got.ContentHash)
	}
}

func TestDictionaryHistoryRepository_SetPromoted_DemotesEveryOtherVersion(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	if err := repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.1", ContentHash: "h1"}); err != nil {
		t.Fatalf("Record 1.0.1: %v", err)
	}
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.2", ContentHash: "h2"}); err != nil {
		t.Fatalf("Record 1.0.2: %v", err)
	}

	if err := repo.SetPromoted(ctx, "1.0.1"); err != nil {
		t.Fatalf("SetPromoted(1.0.1): %v", err)
	}
	if err := repo.SetPromoted(ctx, "1.0.2"); err != nil {
		t.Fatalf("SetPromoted(1.0.2): %v", err)
	}

	first, err := repo.Get(ctx, "1.0.1")
	if err != nil {
		t.Fatalf("Get(1.0.1): %v", err)
	}
	second, err := repo.Get(ctx, "1.0.2")
	if err != nil {
		t.Fatalf("Get(1.0.2): %v", err)
	}

	if first.Promoted {
		t.Error("expected 1.0.1 to be demoted after 1.0.2 was promoted, got Promoted = true")
	}
	if !second.Promoted {
		t.Error("expected 1.0.2 to be promoted, got Promoted = false")
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	promotedCount := 0
	for _, rec := range list {
		if rec.Promoted {
			promotedCount++
		}
	}
	if promotedCount != 1 {
		t.Fatalf("expected exactly 1 promoted version, got %d", promotedCount)
	}
}

func TestDictionaryHistoryRepository_SetPromoted_UnknownVersionIsNoOp(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	if err := repo.Record(ctx, out.DictionaryHistoryRecord{Version: "1.0.1", ContentHash: "h1"}); err != nil {
		t.Fatalf("Record 1.0.1: %v", err)
	}
	if err := repo.SetPromoted(ctx, "1.0.1"); err != nil {
		t.Fatalf("SetPromoted(1.0.1): %v", err)
	}

	if err := repo.SetPromoted(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected SetPromoted with an unknown version to return an error, got nil")
	}

	got, err := repo.Get(ctx, "1.0.1")
	if err != nil {
		t.Fatalf("Get(1.0.1): %v", err)
	}
	if !got.Promoted {
		t.Fatal("expected 1.0.1 to remain promoted after a failed SetPromoted on an unknown version, got Promoted = false")
	}
}

func TestDictionaryHistoryRepository_ReRecordDoesNotResetOperationKind(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: "h0", OperationKind: "rollback",
	}); err != nil {
		t.Fatalf("initial Record: %v", err)
	}

	// Simulate a later backupPreviousDictionary-style call: some other
	// operation makes 1.0.0 the outgoing version and re-Records it with
	// OperationKind hardcoded to "update", regardless of what actually
	// produced 1.0.0. This must not overwrite the true provenance fact.
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.0", ContentHash: "h0", OperationKind: "update",
	}); err != nil {
		t.Fatalf("re-Record: %v", err)
	}

	got, err := repo.Get(ctx, "1.0.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OperationKind != "rollback" {
		t.Fatalf("expected OperationKind to remain %q after re-Record, got %q", "rollback", got.OperationKind)
	}
}

func TestDictionaryHistoryRepository_ReRecordDoesNotResetChangeCountSourceSnapshotRefOrPredecessorVersion(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)
	ctx := context.Background()

	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.1", ContentHash: "h0",
		ChangeCount: 5, SourceSnapshotRef: "fetched-original.json", PredecessorVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("initial Record: %v", err)
	}

	// Simulate backupPreviousDictionary re-recording the outgoing version:
	// it builds a fresh struct literal that leaves ChangeCount,
	// SourceSnapshotRef, and PredecessorVersion at their Go zero values.
	// These first-insert-only provenance fields must not be clobbered.
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: "1.0.1", ContentHash: "h0",
		ChangeCount: 0, SourceSnapshotRef: "", PredecessorVersion: "",
	}); err != nil {
		t.Fatalf("re-Record: %v", err)
	}

	got, err := repo.Get(ctx, "1.0.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ChangeCount != 5 {
		t.Fatalf("expected ChangeCount to remain %d after re-Record, got %d", 5, got.ChangeCount)
	}
	if got.SourceSnapshotRef != "fetched-original.json" {
		t.Fatalf("expected SourceSnapshotRef to remain %q after re-Record, got %q", "fetched-original.json", got.SourceSnapshotRef)
	}
	if got.PredecessorVersion != "1.0.0" {
		t.Fatalf("expected PredecessorVersion to remain %q after re-Record, got %q", "1.0.0", got.PredecessorVersion)
	}
}
