package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestAnonymizeIDs_StableWithinSameDatasetHash(t *testing.T) {
	ids := []string{"article-123", "article-456"}
	a := editorialtrain.AnonymizeIDs(ids, "hash-a")
	b := editorialtrain.AnonymizeIDs(ids, "hash-a")
	if a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("AnonymizeIDs is not stable across calls with the same dataset hash: %v vs %v", a, b)
	}
	if a[0] == "article-123" || a[1] == "article-456" {
		t.Fatal("AnonymizeIDs returned the original IDs unchanged")
	}
	if a[0] == a[1] {
		t.Fatal("two different IDs anonymized to the same value")
	}
}

func TestAnonymizeIDs_DiffersAcrossDatasetHashes(t *testing.T) {
	ids := []string{"article-123"}
	a := editorialtrain.AnonymizeIDs(ids, "hash-a")
	b := editorialtrain.AnonymizeIDs(ids, "hash-b")
	if a[0] == b[0] {
		t.Fatal("same ID anonymized identically under two different dataset hashes, want different (not reversible without the specific dataset)")
	}
}

func TestAnonymizeIDs_EmptyInputReturnsEmpty(t *testing.T) {
	got := editorialtrain.AnonymizeIDs(nil, "hash-a")
	if len(got) != 0 {
		t.Errorf("AnonymizeIDs(nil, ...) = %v, want empty", got)
	}
}
