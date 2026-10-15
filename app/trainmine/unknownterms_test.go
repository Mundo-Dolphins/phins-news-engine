package trainmine_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
)

func TestUnknownTerms(t *testing.T) {
	known := map[string]bool{"isotopes": true, "springfield": true, "coach": true}
	titles := []string{"Springfield Isotopes sign new practice squad linebacker", "Coach discusses roster moves"}

	got := trainmine.UnknownTerms(titles, known)
	// "springfield"/"isotopes"/"coach" are known, "new" (length 3) is short
	// enough to be stopword-sized noise per UnknownTerms' length cutoff;
	// every other distinct word survives, sorted alphabetically.
	want := []string{"discusses", "linebacker", "moves", "practice", "roster", "sign", "squad"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnknownTerms() = %v, want %v (sorted, deduplicated, lowercased, known terms and short words excluded)", got, want)
	}
}

func TestUnknownTerms_EmptyWhenNothingUnknown(t *testing.T) {
	known := map[string]bool{"the": true, "isotopes": true}
	got := trainmine.UnknownTerms([]string{"The Isotopes"}, known)
	if len(got) != 0 {
		t.Errorf("UnknownTerms() = %v, want empty", got)
	}
}
