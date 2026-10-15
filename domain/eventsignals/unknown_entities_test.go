package eventsignals_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func unknownEntityTestRules() eventsignals.Rules {
	return eventsignals.Rules{
		Version: "unknown-entity-test-v1",
		Entities: []eventsignals.EntityRule{
			{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"pierce quenby"}},
			{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes", "springfield isotopes"}},
		},
		FieldWeights:          map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3},
		UnknownEntityStoplist: []string{"Super Bowl"},
	}
}

func TestExtract_UnknownEntities_UnknownTwoWordNameReported(t *testing.T) {
	a := article.Article{ID: "a1", Title: "Isotopes sign CB Unknown Player", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())

	if len(rep.UnknownEntities) != 1 {
		t.Fatalf("UnknownEntities = %+v, want exactly 1 entry", rep.UnknownEntities)
	}
	got := rep.UnknownEntities[0]
	if got.Text != "Unknown Player" {
		t.Errorf("Text = %q, want %q", got.Text, "Unknown Player")
	}
	if got.Field != "title" {
		t.Errorf("Field = %q, want %q", got.Field, "title")
	}
	if got.NormalizedText != "unknown player" {
		t.Errorf("NormalizedText = %q, want %q", got.NormalizedText, "unknown player")
	}
	if got.Reason == "" {
		t.Error("Reason must not be empty")
	}
	if got.PossibleType != "" {
		t.Errorf("PossibleType = %q, want empty — deterministic detection never guesses kind", got.PossibleType)
	}
	if got.Confidence <= 0 || got.Confidence > 1 {
		t.Errorf("Confidence = %v, want within (0,1]", got.Confidence)
	}
}

func TestExtract_UnknownEntities_KnownPlayerNotReported(t *testing.T) {
	a := article.Article{ID: "a2", Title: "Isotopes OT Pierce Quenby returns to practice", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())

	if len(rep.UnknownEntities) != 0 {
		t.Errorf("UnknownEntities = %+v, want none for a fully-cataloged player", rep.UnknownEntities)
	}
	if rep.PrimaryEntity != "Pierce Quenby" {
		t.Fatalf("PrimaryEntity = %q, want %q (sanity check: rules must actually match)", rep.PrimaryEntity, "Pierce Quenby")
	}
}

func TestExtract_UnknownEntities_SingleCapitalizedWordNotReported(t *testing.T) {
	a := article.Article{ID: "a3", Title: "Smith practiced Wednesday", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())

	if len(rep.UnknownEntities) != 0 {
		t.Errorf("UnknownEntities = %+v, want none — single capitalized words are too ambiguous to flag", rep.UnknownEntities)
	}
}

func TestExtract_UnknownEntities_StoplistedPhraseNotReported(t *testing.T) {
	a := article.Article{ID: "a4", Title: "Isotopes eye Super Bowl push", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())

	if len(rep.UnknownEntities) != 0 {
		t.Errorf("UnknownEntities = %+v, want none — %q is stoplisted", rep.UnknownEntities, "Super Bowl")
	}
}

func TestExtract_UnknownEntities_RepeatedCandidateDedupedPerField(t *testing.T) {
	a := article.Article{
		ID:          "a5",
		Title:       "Unknown Player joins Isotopes",
		Body:        "Unknown Player is expected to compete for a roster spot. Unknown Player has drawn praise.",
		PublishedAt: time.Now(),
	}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())

	var titleCount, bodyCount int
	for _, u := range rep.UnknownEntities {
		switch u.Field {
		case "title":
			titleCount++
		case "body":
			bodyCount++
		}
	}
	if titleCount != 1 {
		t.Errorf("title UnknownEntities count = %d, want 1", titleCount)
	}
	if bodyCount != 1 {
		t.Errorf("body UnknownEntities count = %d (candidate repeated twice in body text), want 1 (deduped per field)", bodyCount)
	}
}

// -- run-splitting / early-flush regressions ---

// runSplittingTestRules adds a known coach entity and a "Tuesday" stoplist
// entry to unknownEntityTestRules's catalog, for the punctuation-flush and
// known-term-in-the-middle-of-a-headline regressions below.
func runSplittingTestRules() eventsignals.Rules {
	rules := unknownEntityTestRules()
	rules.Entities = append(rules.Entities, eventsignals.EntityRule{
		ID: "mike-thistlewood", CanonicalName: "Mike Thistlewood", Kind: eventsignals.EntityKindCoach,
		Terms: []string{"coach thistlewood", "mike thistlewood", "thistlewood"},
	})
	rules.UnknownEntityStoplist = append(rules.UnknownEntityStoplist, "Tuesday")
	return rules
}

func TestExtract_UnknownEntities_CommaSeparatesDistinctNames(t *testing.T) {
	a := article.Article{ID: "b1", Title: "Random Newcomer, Another Stranger lead the way", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, runSplittingTestRules(), time.Now())

	got := map[string]bool{}
	for _, u := range rep.UnknownEntities {
		got[u.Text] = true
	}
	want := map[string]bool{"Random Newcomer": true, "Another Stranger": true}
	if len(got) != 2 || !got["Random Newcomer"] || !got["Another Stranger"] {
		t.Fatalf("UnknownEntities texts = %v, want exactly %v (comma must not fuse the two names into one span)", got, want)
	}
}

func TestExtract_UnknownEntities_KnownNamesSeparatedByCommaBothFiltered(t *testing.T) {
	a := article.Article{ID: "b2", Title: "Pierce Quenby, Springfield Isotopes agree to extension", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, runSplittingTestRules(), time.Now())

	if len(rep.UnknownEntities) != 0 {
		t.Errorf("UnknownEntities = %+v, want none — both comma-separated names are known", rep.UnknownEntities)
	}
}

func TestExtract_UnknownEntities_SentenceBoundaryDoesNotLeakStoplistedWord(t *testing.T) {
	a := article.Article{
		ID:          "b3",
		Title:       "Season preview",
		Body:        "The team practiced on Tuesday. Coach Thistlewood said the group looked sharp.",
		PublishedAt: time.Now(),
	}
	rep := eventsignals.Extract(a, runSplittingTestRules(), time.Now())

	for _, u := range rep.UnknownEntities {
		if strings.Contains(u.Text, "Tuesday") {
			t.Errorf("UnknownEntities contains %q: stoplisted %q leaked out of its sentence boundary into a candidate", u.Text, "Tuesday")
		}
	}
	if len(rep.UnknownEntities) != 0 {
		t.Errorf("UnknownEntities = %+v, want none — %q is stoplisted and %q is a known coach", rep.UnknownEntities, "Tuesday", "Coach Thistlewood")
	}
}

func TestExtract_UnknownEntities_HeadlineWithKnownTeamIsolatesUnknownName(t *testing.T) {
	a := article.Article{ID: "b4", Title: "Isotopes Sign Random Newcomer", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, runSplittingTestRules(), time.Now())

	if len(rep.UnknownEntities) != 1 {
		t.Fatalf("UnknownEntities = %+v, want exactly 1 entry", rep.UnknownEntities)
	}
	got := rep.UnknownEntities[0].Text
	if strings.Contains(got, "Isotopes") {
		t.Errorf("UnknownEntities[0].Text = %q, must not contain the known team %q", got, "Isotopes")
	}
	if !strings.Contains(got, "Random Newcomer") {
		t.Errorf("UnknownEntities[0].Text = %q, want it to contain %q", got, "Random Newcomer")
	}
}

func unknownTexts(rep eventsignals.Representation) []string {
	var out []string
	for _, u := range rep.UnknownEntities {
		out = append(out, u.Text)
	}
	return out
}

func TestExtract_UnknownEntities_NFLNameSpans(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"ascii apostrophe", "Isotopes sign Da'Rel Ashcombe", []string{"Da'Rel Ashcombe"}},
		{"typographic apostrophe", "Isotopes sign Da’Rel Ashcombe", []string{"Da’Rel Ashcombe"}},
		{"Ja'Ron", "Ja'Ron Quillfeather scores", []string{"Ja'Ron Quillfeather"}},
		{"D'Ambry", "Isotopes sign D'Ambry Thistlecombe", []string{"D'Ambry Thistlecombe"}},
		{"Ka'ulo", "Isotopes sign Ka'ulo Fernsby", []string{"Ka'ulo Fernsby"}},
		{"camel", "Isotopes sign ZoZo Pemberly-Quist", []string{"ZoZo Pemberly-Quist"}},
		{"hyphen U+2010", "Isotopes sign Quenton Varga‐Sollis", []string{"Quenton Varga‐Sollis"}},
		{"hyphen U+2011", "Isotopes sign Quenton Varga‑Sollis", []string{"Quenton Varga‑Sollis"}},
		{"Elan-Ra St. Quillon", "Isotopes sign Elan-Ra St. Quillon", []string{"Elan-Ra St. Quillon"}},
		{"A.J.", "Isotopes sign Q.T. Brindlewood", []string{"Q.T. Brindlewood"}},
		{"D.J.", "Isotopes sign Z.K. Marlowby", []string{"Z.K. Marlowby"}},
		{"C.J.", "Isotopes sign X.V. Strandlow", []string{"X.V. Strandlow"}},
		{"T.J.", "Isotopes sign V.Q. Hollenbrook", []string{"V.Q. Hollenbrook"}},
		{"Jr.", "Isotopes sign Orvel Harrowgate Jr.", []string{"Orvel Harrowgate Jr."}},
		{"II", "Isotopes sign Ambrose Quilling II", []string{"Ambrose Quilling II"}},
		{"contraction I'm", "I'm Ready", nil},
		{"contraction I've", "I've Seen Enough", nil},
		{"abbreviation isolates name", "NFL CB Unknown Player", []string{"Unknown Player"}},
		{"comma separates", "Random Newcomer, Another Stranger", []string{"Random Newcomer", "Another Stranger"}},
		{"slash separates", "Random Newcomer/Another Stranger", nil},
		{"suffix needs plausible name", "Newcomer II", nil},
	}
	for _, tc := range tests {
		for _, field := range []string{"title", "body"} {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				a := article.Article{ID: "x", PublishedAt: time.Now()}
				if field == "title" {
					a.Title = tc.text
				} else {
					a.Title = "headline"
					a.Body = tc.text
				}
				got := unknownTexts(eventsignals.Extract(a, unknownEntityTestRules(), time.Now()))
				if strings.Join(got, "|") != strings.Join(tc.want, "|") {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestExtract_UnknownEntities_TypographicVariantsOneIdentity(t *testing.T) {
	a := article.Article{ID: "x", Title: "Da'Rel Ashcombe", Body: "Da’Rel Ashcombe", PublishedAt: time.Now()}
	rep := eventsignals.Extract(a, unknownEntityTestRules(), time.Now())
	if len(rep.UnknownEntities) != 2 {
		t.Fatalf("got %+v, want 2 (title+body)", rep.UnknownEntities)
	}
	if rep.UnknownEntities[0].NormalizedText != rep.UnknownEntities[1].NormalizedText {
		t.Errorf("normalized differ: %+v", rep.UnknownEntities)
	}
}

func TestExtract_UnknownEntities_CatalogMatchesTypographicVariant(t *testing.T) {
	rules := unknownEntityTestRules()
	rules.Entities = append(rules.Entities, eventsignals.EntityRule{ID: "devon", CanonicalName: "Da'Rel Ashcombe", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Da'Rel Ashcombe", "Q.T. Brindlewood"}})
	for _, text := range []string{"Isotopes praise Da’Rel Ashcombe", "Isotopes praise Q.T. Brindlewood", "Isotopes praise Quenton Varga-Sollis"} {
		a := article.Article{ID: "x", Title: text, PublishedAt: time.Now()}
		rep := eventsignals.Extract(a, rules, time.Now())
		if strings.Contains(text, "Varga") {
			continue
		}
		if len(rep.UnknownEntities) != 0 {
			t.Errorf("%q: unknown = %+v, want none", text, rep.UnknownEntities)
		}
		if len(rep.Entities) < 2 {
			t.Errorf("%q: known entity not matched: %+v", text, rep.Entities)
		}
	}
}
