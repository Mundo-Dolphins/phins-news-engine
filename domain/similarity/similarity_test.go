package similarity_test

import (
	"math"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/similarity"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestJaccard_IdenticalSetsScoreOne(t *testing.T) {
	a := []string{"isotopes beat", "beat sharks"}
	if got := similarity.Jaccard(a, a); !almostEqual(got, 1.0) {
		t.Errorf("Jaccard(identical) = %v, want 1.0", got)
	}
}

func TestJaccard_DisjointSetsScoreZero(t *testing.T) {
	got := similarity.Jaccard([]string{"a b"}, []string{"c d"})
	if !almostEqual(got, 0.0) {
		t.Errorf("Jaccard(disjoint) = %v, want 0.0", got)
	}
}

func TestJaccard_PartialOverlap(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"b", "c", "d"}
	// intersection = {b,c} = 2, union = {a,b,c,d} = 4
	got := similarity.Jaccard(a, b)
	if !almostEqual(got, 0.5) {
		t.Errorf("Jaccard(partial) = %v, want 0.5", got)
	}
}

func TestJaccard_DuplicateElementsAreDeduped(t *testing.T) {
	a := []string{"x", "x", "y"}
	b := []string{"x", "y", "y"}
	got := similarity.Jaccard(a, b)
	if !almostEqual(got, 1.0) {
		t.Errorf("Jaccard(with dupes) = %v, want 1.0 (sets, not multisets)", got)
	}
}

func TestJaccard_BothEmptyScoresZero(t *testing.T) {
	if got := similarity.Jaccard(nil, nil); !almostEqual(got, 0.0) {
		t.Errorf("Jaccard(empty, empty) = %v, want 0.0", got)
	}
}

func TestDice_IdenticalSetsScoreOne(t *testing.T) {
	a := []string{"a", "b"}
	if got := similarity.Dice(a, a); !almostEqual(got, 1.0) {
		t.Errorf("Dice(identical) = %v, want 1.0", got)
	}
}

func TestDice_PartialOverlap(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"b", "c", "d"}
	// 2 * |intersection| / (|a|+|b|) = 2*2/6 = 0.666...
	got := similarity.Dice(a, b)
	if !almostEqual(got, 2.0/3.0) {
		t.Errorf("Dice(partial) = %v, want %v", got, 2.0/3.0)
	}
}

func TestDice_BothEmptyScoresZero(t *testing.T) {
	if got := similarity.Dice(nil, nil); !almostEqual(got, 0.0) {
		t.Errorf("Dice(empty, empty) = %v, want 0.0", got)
	}
}

func TestJaccard_DifferentLanguagesNoSharedShinglesScoreZero(t *testing.T) {
	english := []string{"isotopes win", "win big"}
	spanish := []string{"delfines ganan", "ganan grande"}
	got := similarity.Jaccard(english, spanish)
	if !almostEqual(got, 0.0) {
		t.Errorf("Jaccard(cross-language) = %v, want 0.0 (no language-specific handling needed)", got)
	}
}

func TestContainment(t *testing.T) {
	if got := similarity.Containment([]string{"a", "b"}, []string{"a", "b", "c", "d"}); !almostEqual(got, 1.0) {
		t.Errorf("Containment(subset) = %v, want 1.0", got)
	}
	if got := similarity.Containment([]string{"a", "x"}, []string{"a", "b"}); !almostEqual(got, 0.5) {
		t.Errorf("Containment(partial) = %v, want 0.5", got)
	}
	if got := similarity.Containment(nil, []string{"a"}); !almostEqual(got, 0) {
		t.Errorf("Containment(empty) = %v, want 0", got)
	}
}
