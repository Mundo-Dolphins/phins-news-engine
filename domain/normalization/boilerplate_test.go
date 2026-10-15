package normalization_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

func TestStripPhrases_RemovesEachPhraseCaseInsensitively(t *testing.T) {
	got := normalization.StripPhrases(
		"Isotopes beat the Sharks. Subscribe to our channel. Follow us on Facebook. More at the link.",
		[]string{"subscribe to our channel.", "follow us on facebook."},
	)
	want := "Isotopes beat the Sharks. More at the link."
	if got != want {
		t.Errorf("StripPhrases() = %q, want %q", got, want)
	}
}

func TestStripPhrases_NoPhrasesMatchedLeavesTextUnchanged(t *testing.T) {
	got := normalization.StripPhrases("Isotopes sign new WR", []string{"get the app"})
	want := "Isotopes sign new WR"
	if got != want {
		t.Errorf("StripPhrases() = %q, want %q", got, want)
	}
}

func TestStripPhrases_CollapsesWhitespaceLeftBehind(t *testing.T) {
	got := normalization.StripPhrases("Breaking news   Get the App   more coverage", []string{"Get the App"})
	want := "Breaking news more coverage"
	if got != want {
		t.Errorf("StripPhrases() = %q, want %q", got, want)
	}
}
