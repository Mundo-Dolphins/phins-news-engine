package entityproposal_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestNormalize_CollapsesTypographicVariants(t *testing.T) {
	cases := []string{
		"Jai'Rene' Mossbank",
		"Jai’Rene’ Mossbank", // curly apostrophes
		"JaiRene Mossbank",
	}
	want := entityproposal.Normalize(cases[0])
	for _, c := range cases[1:] {
		if got := entityproposal.Normalize(c); got != want {
			t.Errorf("Normalize(%q) = %q, want %q (should match Normalize(%q))", c, got, want, cases[0])
		}
	}
}

func TestNormalize_LowercasesAndCollapsesWhitespace(t *testing.T) {
	if got, want := entityproposal.Normalize("  Coach   Harrowby "), "coach harrowby"; got != want {
		t.Errorf("Normalize() = %q, want %q", got, want)
	}
}

func TestSurname_ReturnsLastToken(t *testing.T) {
	if got, want := entityproposal.Surname("Jeff Harrowby"), "harrowby"; got != want {
		t.Errorf("Surname() = %q, want %q", got, want)
	}
}

func TestSurname_EmptyForSingleWordName(t *testing.T) {
	if got := entityproposal.Surname("Isotopes"); got != "" {
		t.Errorf("Surname() = %q, want empty for a single-word name", got)
	}
}
