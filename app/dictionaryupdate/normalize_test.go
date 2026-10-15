package dictionaryupdate_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

func TestCanonicalKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "Ezo Quarrington", "ezo quarrington"},
		{"apostrophe", "Da'Rel Ashcombe", "da'rel ashcombe"},
		{"hyphen", "Robert Jones-Smith", "robert jones-smith"},
		{"accents", "Raúl Ruíz", "raul ruiz"},
		{"suffix jr", "Oswin Brackley Jr.", "oswin brackley"},
		{"suffix ii", "Conrad Wexbury II", "conrad wexbury"},
		{"extra whitespace", "  Jaylen   Wickersham ", "jaylen wickersham"},
		{"uppercase", "MIKE THISTLEWOOD", "mike thistlewood"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dictionaryupdate.CanonicalKey(tc.input)
			if got != tc.want {
				t.Errorf("CanonicalKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyAlias(t *testing.T) {
	cases := []struct {
		name      string
		canonical string
		alias     string
		want      dictionaryupdate.AliasClass
	}{
		{"exact match", "Jaylen Wickersham", "Jaylen Wickersham", dictionaryupdate.AliasClassExact},
		{"bare surname is ambiguous", "Tavion Dale", "Dale", dictionaryupdate.AliasClassAmbiguous},
		{"bare first name is ambiguous", "Tavion Dale", "Tavion", dictionaryupdate.AliasClassAmbiguous},
		{"bare middle name is ambiguous", "Robert Kenneth Griffin", "Kenneth", dictionaryupdate.AliasClassAmbiguous},
		{"nickname first+last is suggested, not auto-confirmed", "Michael Quint", "Mike Quint", dictionaryupdate.AliasClassSuggested},
		{"initial+last", "Jaylen Wickersham", "J. Wickersham", dictionaryupdate.AliasClassInitialLast},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dictionaryupdate.ClassifyAlias(tc.canonical, tc.alias)
			if got != tc.want {
				t.Errorf("ClassifyAlias(%q, %q) = %q, want %q", tc.canonical, tc.alias, got, tc.want)
			}
		})
	}
}
