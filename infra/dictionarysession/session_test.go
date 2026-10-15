package dictionarysession_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
)

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	proposals := []dictionaryupdate.Proposal{{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a player"}}
	s := dictionarysession.New("sess-1", []string{"fetched-x.json"}, "1.2.3", "hash-abc", proposals, time.Now())

	if err := dictionarysession.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := dictionarysession.Load(dir, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != s.ID || got.InitialVersion != "1.2.3" || len(got.Proposals) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Status != dictionarysession.StatusOpen {
		t.Errorf("Status = %q, want open", got.Status)
	}
}

func TestSave_PersistsDecisionsImmediately(t *testing.T) {
	dir := t.TempDir()
	s := dictionarysession.New("sess-2", nil, "1.0.0", "hash", nil, time.Now())
	s.Decisions = append(s.Decisions, dictionarysession.Decision{ProposalIndex: 0, Action: dictionarysession.DecisionAccept, DecidedAt: time.Now()})
	s.Cursor = 1

	if err := dictionarysession.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := dictionarysession.Load(dir, "sess-2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Decisions) != 1 || got.Decisions[0].Action != dictionarysession.DecisionAccept || got.Cursor != 1 {
		t.Fatalf("decision/cursor did not persist: %+v", got)
	}
}

func TestLatestOpen_ReturnsMostRecentlyUpdatedOpenSession(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// Create session that was created recently but not touched since (stale).
	// This session's CreatedAt is more recent, but UpdatedAt is old.
	older := dictionarysession.New("old", nil, "1.0.0", "h", nil, now)
	older.UpdatedAt = now.Add(-time.Hour) // updated long ago; created recently

	// Create session that was created long ago but recently touched (active).
	// This session's CreatedAt is old, but UpdatedAt is most recent.
	newer := dictionarysession.New("new", nil, "1.0.0", "h", nil, now.Add(-2*time.Hour))
	newer.UpdatedAt = now // updated very recently; created long ago

	if err := dictionarysession.Save(dir, older); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	if err := dictionarysession.Save(dir, newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}

	got, err := dictionarysession.LatestOpen(dir)
	if err != nil {
		t.Fatalf("LatestOpen: %v", err)
	}
	if got.ID != "new" {
		t.Errorf("LatestOpen ID = %q, want %q (should pick by UpdatedAt, not CreatedAt)", got.ID, "new")
	}
}

func TestLatestOpen_IgnoresCompletedSessions(t *testing.T) {
	dir := t.TempDir()
	s := dictionarysession.New("done", nil, "1.0.0", "h", nil, time.Now())
	s.Status = dictionarysession.StatusComplete
	if err := dictionarysession.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := dictionarysession.LatestOpen(dir); err == nil {
		t.Fatal("expected an error when no open session exists")
	}
}

func TestDiscard_RemovesSessionFile(t *testing.T) {
	dir := t.TempDir()
	s := dictionarysession.New("to-discard", nil, "1.0.0", "h", nil, time.Now())
	if err := dictionarysession.Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := dictionarysession.Discard(dir, "to-discard"); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := dictionarysession.Load(dir, "to-discard"); err == nil {
		t.Fatal("expected Load to fail after Discard")
	}
}
