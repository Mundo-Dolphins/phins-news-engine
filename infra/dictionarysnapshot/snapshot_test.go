package dictionarysnapshot_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
)

func TestNew_SortsEntitiesDeterministically(t *testing.T) {
	e1 := dictionaryupdate.SourceEntity{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "zzz player"}
	e2 := dictionaryupdate.SourceEntity{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "aaa player"}

	snap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{e1, e2}, nil, time.Now())

	if snap.Entities[0].CanonicalKey != "aaa player" {
		t.Fatalf("expected deterministic sort by canonical key, got %+v", snap.Entities)
	}
}

func TestNew_ContentHashIndependentOfOrderAndFetchedAt(t *testing.T) {
	e1 := dictionaryupdate.SourceEntity{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "a player"}
	e2 := dictionaryupdate.SourceEntity{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "b player"}

	s1 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{e1, e2}, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s2 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{e2, e1}, nil, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	if s1.ContentHash != s2.ContentHash {
		t.Fatalf("ContentHash should be independent of input order and FetchedAt")
	}
}

func TestWriteThenRead_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	snap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "a player", DisplayName: "A Player"}},
		nil, time.Now())

	path, err := dictionarysnapshot.Write(dir, snap, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "fetched-roster-20260801-090000.json" {
		t.Fatalf("unexpected filename: %s", filepath.Base(path))
	}

	got, err := dictionarysnapshot.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ContentHash != snap.ContentHash {
		t.Fatalf("ContentHash mismatch after round-trip: %q vs %q", got.ContentHash, snap.ContentHash)
	}
	if len(got.Entities) != 1 || got.Entities[0].DisplayName != "A Player" {
		t.Fatalf("entities did not round-trip: %+v", got.Entities)
	}
}

func TestWrite_TwoRunsSameContent_SameHashDifferentFilenames(t *testing.T) {
	dir := t.TempDir()
	entities := []dictionaryupdate.SourceEntity{{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "a player"}}

	snap1 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, entities, nil, time.Now())
	snap2 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200, entities, nil, time.Now())

	path1, err := dictionarysnapshot.Write(dir, snap1, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	path2, err := dictionarysnapshot.Write(dir, snap2, time.Date(2026, 8, 1, 9, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if path1 == path2 {
		t.Fatalf("expected different filenames, got the same: %s", path1)
	}
	if snap1.ContentHash != snap2.ContentHash {
		t.Fatalf("expected identical content hash for identical entity content")
	}
}

func TestNew_ContentHashIgnoresObservedAt(t *testing.T) {
	now1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	now2 := now1.Add(time.Hour)

	e1 := dictionaryupdate.SourceEntity{
		Kind:         dictionaryupdate.SourceKindRoster,
		CanonicalKey: "a player",
		DisplayName:  "A Player",
		ObservedAt:   now1,
	}
	e2 := dictionaryupdate.SourceEntity{
		Kind:         dictionaryupdate.SourceKindRoster,
		CanonicalKey: "a player",
		DisplayName:  "A Player",
		ObservedAt:   now2,
	}

	s1 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{e1}, nil, time.Now())
	s2 := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x", "x", "auto", 200,
		[]dictionaryupdate.SourceEntity{e2}, nil, time.Now())

	if s1.ContentHash != s2.ContentHash {
		t.Fatalf("ContentHash should be independent of ObservedAt: %q vs %q", s1.ContentHash, s2.ContentHash)
	}
}

func TestWrite_MultipleSourcesSameTimestamp_NoCollision(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	// Write three different sources with identical timestamp
	rosterSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://roster", "roster", "auto", 200,
		[]dictionaryupdate.SourceEntity{{Kind: dictionaryupdate.SourceKindRoster, CanonicalKey: "a player"}},
		nil, time.Now())
	coachesSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindCoaches, "https://coaches", "coaches", "auto", 200,
		[]dictionaryupdate.SourceEntity{{Kind: dictionaryupdate.SourceKindCoaches, CanonicalKey: "a coach"}},
		nil, time.Now())
	foSnap := dictionarysnapshot.New(dictionaryupdate.SourceKindFrontOffice, "https://fo", "fo", "auto", 200,
		[]dictionaryupdate.SourceEntity{{Kind: dictionaryupdate.SourceKindFrontOffice, CanonicalKey: "an exec"}},
		nil, time.Now())

	path1, err := dictionarysnapshot.Write(dir, rosterSnap, now)
	if err != nil {
		t.Fatalf("Write roster: %v", err)
	}
	path2, err := dictionarysnapshot.Write(dir, coachesSnap, now)
	if err != nil {
		t.Fatalf("Write coaches: %v", err)
	}
	path3, err := dictionarysnapshot.Write(dir, foSnap, now)
	if err != nil {
		t.Fatalf("Write front_office: %v", err)
	}

	// All paths must be different
	if path1 == path2 || path2 == path3 || path1 == path3 {
		t.Fatalf("expected different filenames for different sources at same timestamp:\n  path1=%s\n  path2=%s\n  path3=%s",
			filepath.Base(path1), filepath.Base(path2), filepath.Base(path3))
	}

	// Verify filenames include source discriminators
	if filepath.Base(path1) != "fetched-roster-20260801-090000.json" {
		t.Fatalf("expected roster filename, got %s", filepath.Base(path1))
	}
	if filepath.Base(path2) != "fetched-coaches-20260801-090000.json" {
		t.Fatalf("expected coaches filename, got %s", filepath.Base(path2))
	}
	if filepath.Base(path3) != "fetched-front_office-20260801-090000.json" {
		t.Fatalf("expected front_office filename, got %s", filepath.Base(path3))
	}

	// Verify all files exist and can be read back
	got1, err := dictionarysnapshot.Read(path1)
	if err != nil {
		t.Fatalf("Read roster: %v", err)
	}
	if got1.Source != dictionaryupdate.SourceKindRoster {
		t.Fatalf("expected roster source, got %v", got1.Source)
	}

	got2, err := dictionarysnapshot.Read(path2)
	if err != nil {
		t.Fatalf("Read coaches: %v", err)
	}
	if got2.Source != dictionaryupdate.SourceKindCoaches {
		t.Fatalf("expected coaches source, got %v", got2.Source)
	}

	got3, err := dictionarysnapshot.Read(path3)
	if err != nil {
		t.Fatalf("Read front_office: %v", err)
	}
	if got3.Source != dictionaryupdate.SourceKindFrontOffice {
		t.Fatalf("expected front_office source, got %v", got3.Source)
	}
}
