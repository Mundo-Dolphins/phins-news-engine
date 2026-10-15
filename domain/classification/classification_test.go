package classification_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

// buildMatcher mirrors a trimmed-down version of the real seed dictionary's
// category shape: strong team/URL signals, an ambiguous "isotopes"/"topes"
// term that requires NFL context, generic NFL context terms, a bulk
// people category, a rivals category, and the three negative categories —
// enough to exercise every rule classification.Decide can reach.
func buildMatcher(t *testing.T) dictionary.Matcher {
	t.Helper()
	d := dictionary.Dictionary{
		Version: "test-v1",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{
				{CanonicalName: "Springfield Isotopes", Source: "manual"},
			}},
			{Name: "url_patterns", Weight: 20, StrongOverride: true, Entries: []dictionary.Entry{
				{CanonicalName: "isotopes.example.com", Source: "manual"},
			}},
			{Name: "nfl_context_terms", Weight: 3, Entries: []dictionary.Entry{
				{CanonicalName: "injury report", Source: "v1"}, {CanonicalName: "draft", Source: "v1"},
			}},
			{Name: "nfl_people_context", Weight: 6, Entries: []dictionary.Entry{
				{CanonicalName: "Ezo Quarrington", Aliases: []string{"Ezo"}, Source: "v1"},
			}},
			// Weighted lower than nfl_people_context: a former player's name
			// alone is weaker evidence of *current* relevance
			// ("un exjugador no implica siempre relevancia actual"). At weight
			// 2, even a title mention (2 * title_weight_multiplier 2 = 4)
			// stays below positive_threshold_medium (7 in this fixture).
			{Name: "alumni", Weight: 2, Entries: []dictionary.Entry{
				{CanonicalName: "Dan Pellingham", Source: "manual"},
			}},
			{Name: "rivals", Weight: 1, Entries: []dictionary.Entry{
				{CanonicalName: "Shelbyville Sharks", Source: "v1"},
			}},
			{
				Name: "ambiguous_team_terms", Weight: 4,
				RequiresAnyOf: []string{"nfl_context_terms", "nfl_people_context", "official_names", "url_patterns"},
				Entries:       []dictionary.Entry{{CanonicalName: "isotopes", Source: "v1"}, {CanonicalName: "topes", Source: "v1"}},
			},
			{Name: "negative_animal_terms", Weight: -12, Entries: []dictionary.Entry{
				{CanonicalName: "aquarium", Source: "v1"},
				{CanonicalName: "isotope watching", Source: "v1"},
				{CanonicalName: "blue whale", Source: "manual"},
			}},
			// Only a compound team hashtag is unambiguous on its own.
			{Name: "hashtags", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{
				{CanonicalName: "#SprIsotopes", Source: "manual"},
			}},
			// A bare "#Isotopes"/"#Isos" hashtag carries the exact same
			// team/animal ambiguity as the plain words above (production
			// evidence: the same source posted both "#Isotopes vow to stay
			// committed to running the ball" (team) and "#whales #isotopes
			// Isotopes Flip for Blue Whale" (animal)). So it contributes
			// ordinary positive weight instead of strong_override, letting
			// negative_animal_terms outweigh it when animal context is also
			// present — see TestDecide_BareHashtagWithoutOtherContextIsRelated
			// and TestDecide_HashtaggedAnimalContentIsNotFalselyRelated.
			{Name: "team_hashtags", Weight: 6, Entries: []dictionary.Entry{
				{CanonicalName: "#Isotopes", Source: "manual"},
			}},
			{Name: "negative_local_springfield_terms", Weight: -6, Entries: []dictionary.Entry{
				{CanonicalName: "arrested", Source: "v1"},
			}},
			{Name: "negative_other_sports_terms", Weight: -6, Entries: []dictionary.Entry{
				{CanonicalName: "nba finals", Source: "v1"},
			}},
		},
		Decision: dictionary.Decision{
			// PositiveThresholdMedium is 7, not 6: nfl_context_terms' weight
			// (3) * TitleWeightMultiplier (2) equals exactly 6, so a single
			// lone title mention of a generic context term like "draft"
			// (no RequiresAnyOf, no other evidence) would otherwise clear
			// PositiveThresholdMedium by coincidence of that weight*multiplier
			// product, contradicting TestDecide_GenericNFLTermAloneIsNotRelated.
			// nfl_context_terms' weight can't move instead — it's pinned by
			// TestDecide_MaxCountPerTermCapsRepeatedMentions' exact Score==30.
			PositiveThresholdHigh: 10, PositiveThresholdMedium: 7, NegativeThresholdHigh: 0,
			TitleWeightMultiplier: 2, MaxCountPerTerm: 3,
		},
	}
	m, err := dictionary.Prepare(d)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return m
}

func TestDecide_StrongTeamNamePhraseIsRelatedByStrongOverride(t *testing.T) {
	got := classification.Decide("Springfield Isotopes sign veteran cornerback", "", buildMatcher(t))
	if !got.Related || got.RuleApplied != classification.RuleStrongOverride {
		t.Errorf("Related=%v RuleApplied=%q, want Related=true RuleStrongOverride", got.Related, got.RuleApplied)
	}
}

func TestDecide_OfficialURLPatternIsRelatedByStrongOverride(t *testing.T) {
	got := classification.Decide("Team notes", "Full story at https://www.isotopes.example.com/news/story", buildMatcher(t))
	if !got.Related || got.RuleApplied != classification.RuleStrongOverride {
		t.Errorf("Related=%v RuleApplied=%q, want Related=true RuleStrongOverride (a trusted source can use a short title without the team name)", got.Related, got.RuleApplied)
	}
}

func TestDecide_CurrentPlayerNameAliasWithNFLContextIsRelated(t *testing.T) {
	got := classification.Decide("Ezo Quarrington injury report ahead of Sunday", "", buildMatcher(t))
	if !got.Related {
		t.Error("want related: a current player's full name plus NFL context terms")
	}
	found := false
	for _, e := range got.PositiveEvidence {
		if e.EntryName == "Ezo Quarrington" {
			found = true
		}
	}
	if !found {
		t.Errorf("PositiveEvidence = %+v, want an entry for Ezo Quarrington", got.PositiveEvidence)
	}
}

func TestDecide_AliasAloneWithContextIsRelated(t *testing.T) {
	got := classification.Decide("Ezo injury report ahead of Sunday", "", buildMatcher(t))
	if !got.Related {
		t.Error("want related: the alias 'Ezo' plus an NFL context term")
	}
}

func TestDecide_AmbiguousTermAloneIsNotRelated(t *testing.T) {
	got := classification.Decide("Isotopes spotted near the coast", "", buildMatcher(t))
	if got.Related {
		t.Errorf("Related=true, want false: 'isotopes' alone (no NFL context) is the animal-ambiguity case from the prompt")
	}
	if got.RuleApplied != classification.RuleBelowThreshold {
		t.Errorf("RuleApplied = %q, want %q", got.RuleApplied, classification.RuleBelowThreshold)
	}
}

// A compound team hashtag like #SprIsotopes is unambiguous by itself, the
// same tier as the "Springfield Isotopes" phrase in official_names — nobody
// hashtags actual isotopes-the-animal as #SprIsotopes.
func TestDecide_CompoundTeamHashtagIsRelatedByStrongOverride(t *testing.T) {
	got := classification.Decide("Big news for the #SprIsotopes today", "", buildMatcher(t))
	if !got.Related || got.RuleApplied != classification.RuleStrongOverride {
		t.Errorf("Related=%v RuleApplied=%q, want Related=true RuleStrongOverride", got.Related, got.RuleApplied)
	}
}

// Regression test for a production false negative — headlines like
// "#Isotopes vow to stay committed to running the ball" scored 0.00 with
// "entities detected: none" because the only match was the bare, ambiguous
// "isotopes" term (a "#Isotopes" hashtag is textually the same ambiguous
// word, just hashtagged) and no other category satisfied its
// RequiresAnyOf gate. Fixed via the non-strong-override team_hashtags
// category (ordinary positive weight, not a bypass) rather than making
// "#Isotopes" strong-override outright — see
// TestDecide_HashtaggedAnimalContentIsNotFalselyRelated for why that would
// have been unsafe.
func TestDecide_BareHashtagWithoutOtherContextIsRelated(t *testing.T) {
	got := classification.Decide("#Isotopes vow to stay committed to running the ball", "", buildMatcher(t))
	if !got.Related {
		t.Errorf("Related=false RuleApplied=%q, want true: a bare #Isotopes hashtag should be enough team-context evidence on its own", got.RuleApplied)
	}
	if got.RuleApplied == classification.RuleStrongOverride {
		t.Errorf("RuleApplied = %q, want an ordinary threshold rule, not strong_override: a bare #Isotopes hashtag must stay overridable by negative_animal_terms", got.RuleApplied)
	}
}

// Guards the reasoning behind not making "#Isotopes"/"#Isos" strong
// override: unlike "#SprIsotopes", a bare team hashtag is exactly as
// ambiguous as the plain word (production evidence: the same source posted
// both the team headline above and this one, about the actual animal) and
// must not bypass negative evidence the way strong_override would.
func TestDecide_HashtaggedAnimalContentIsNotFalselyRelated(t *testing.T) {
	got := classification.Decide("#whales #isotopes Isotopes Flip for Blue Whale | Dana Point, CA", "", buildMatcher(t))
	if got.Related {
		t.Errorf("Related=true RuleApplied=%q, want false: hashtagging the animal, corroborated by an accompanying #whales tag and \"Blue Whale\", must not be treated as team context", got.RuleApplied)
	}
}

func TestDecideWithSourceContext_AcceptsExplicitTeamTermWithoutBypassingScoring(t *testing.T) {
	matcher := buildMatcher(t)

	got := classification.DecideWithSourceContext("Isotopes practicing in Napa", "", matcher)
	if !got.Related || got.RuleApplied != classification.RuleSourceContext {
		t.Fatalf("Related=%v RuleApplied=%q Score=%.2f, want source-context acceptance", got.Related, got.RuleApplied, got.Score)
	}

	noEvidence := classification.DecideWithSourceContext("College football notes", "", matcher)
	if noEvidence.Related {
		t.Fatalf("Related=true Score=%.2f, want rejection without explicit Isotopes evidence", noEvidence.Score)
	}

	negative := classification.DecideWithSourceContext("Isotopes arrested near aquarium", "", matcher)
	if negative.Related {
		t.Fatalf("Related=true Score=%.2f, want negative evidence to remain effective", negative.Score)
	}
}

func TestDecide_AmbiguousTermWithNFLContextIsRelated(t *testing.T) {
	got := classification.Decide("Isotopes injury report: three starters questionable", "", buildMatcher(t))
	if !got.Related {
		t.Error("want related: 'isotopes' + 'injury report' clears the ambiguity requirement")
	}
}

func TestDecide_AlumnusAloneContributesButDoesNotAloneMeetHighThreshold(t *testing.T) {
	got := classification.Decide("Dan Pellingham inducted into another hall of fame", "", buildMatcher(t))
	if got.Related {
		t.Errorf("Related=true (score %.2f), want false: a former player's name alone (alumni weight 2, even title-weighted stays below the medium threshold of 7 in this fixture) needs help from more evidence — a former player mention doesn't always imply current relevance", got.Score)
	}
}

func TestDecide_RivalTeamMentionedIncidentallyIsNotRelatedAlone(t *testing.T) {
	got := classification.Decide("Shelbyville Sharks sign a new kicker", "", buildMatcher(t))
	if got.Related {
		t.Error("Related=true, want false: a rival mentioned with no Isotopes evidence at all must not be related")
	}
}

func TestDecide_GenericNFLTermAloneIsNotRelated(t *testing.T) {
	got := classification.Decide("NFL draft coverage begins this week", "", buildMatcher(t))
	if got.Related {
		t.Error("Related=true, want false: a generic NFL context term with no team/people evidence")
	}
}

func TestDecide_HeadlineWithoutTeamNameButWithPlayerAndContextIsRelated(t *testing.T) {
	got := classification.Decide("Ezo Quarrington: latest injury report", "", buildMatcher(t))
	if !got.Related {
		t.Error("want related: a headline that never says 'Springfield Isotopes' but names a current player plus context")
	}
}

func TestDecide_AnimalFalsePositiveFromV1HistoryIsRejectedByNegativeEvidence(t *testing.T) {
	got := classification.Decide("Isotopes spotted: local aquarium hosts isotope watching event", "", buildMatcher(t))
	if got.Related {
		t.Errorf("Related=true (score %.2f), want false: this is the v1 animal false positive regression case", got.Score)
	}
	if got.RuleApplied != classification.RuleNegative {
		t.Errorf("RuleApplied = %q, want %q", got.RuleApplied, classification.RuleNegative)
	}
}

func TestDecide_LocalSpringfieldNewsFalsePositiveFromV1HistoryIsRejected(t *testing.T) {
	got := classification.Decide("Suspect arrested after Springfield Gardens incident", "", buildMatcher(t))
	if got.Related {
		t.Error("Related=true, want false: this is the v1 local-Springfield-news false positive regression case")
	}
}

func TestDecide_OtherSportsFalsePositiveFromV1HistoryIsRejected(t *testing.T) {
	got := classification.Decide("Springfield's other pro teams: NBA Finals recap", "", buildMatcher(t))
	if got.Related {
		t.Error("Related=true, want false: this is the v1 other-sports false positive regression case")
	}
}

func TestDecide_TitleOccurrencesAreWeightedMoreThanBodyOccurrences(t *testing.T) {
	titleHeavy := classification.Decide("Isotopes injury report", "routine update", buildMatcher(t))
	bodyOnly := classification.Decide("routine update", "Isotopes injury report", buildMatcher(t))
	if titleHeavy.Score <= bodyOnly.Score {
		t.Errorf("titleHeavy.Score = %.2f, bodyOnly.Score = %.2f, want title occurrences to score higher (title_weight_multiplier=2)", titleHeavy.Score, bodyOnly.Score)
	}
}

func TestDecide_MaxCountPerTermCapsRepeatedMentions(t *testing.T) {
	repeated := "Isotopes Isotopes Isotopes Isotopes Isotopes injury report"
	got := classification.Decide(repeated, "", buildMatcher(t))
	for _, e := range got.PositiveEvidence {
		if e.EntryName == "isotopes" && e.Count > 5 {
			t.Errorf("raw Count = %d reported correctly, but Contribution should reflect max_count_per_term=3 cap: %+v", e.Count, e)
		}
	}
	// 5 title mentions of "isotopes" (weight 4) capped at max_count_per_term=3,
	// weighted by title_weight_multiplier=2: 4 * 3 * 2 = 24 from "isotopes"
	// alone, plus "injury report" (weight 3, capped at 1) * 2 = 6, totalling 30 —
	// if the cap were NOT applied it would be 4*5*2=40+6=46. Assert the capped
	// total, not the uncapped one.
	if got.Score != 30 {
		t.Errorf("Score = %.2f, want 30 (max_count_per_term cap applied before title weighting)", got.Score)
	}
}

func TestDecide_TrustedOfficialSourceShortTitleStillNeedsCallerBypass(t *testing.T) {
	// classification.Decide itself has no notion of "trusted" — that's a
	// usecase-layer concern (Task 7). A short, official-sounding title with
	// zero dictionary evidence is NOT related on its own; this documents
	// why the bypass exists rather than asserting bypass behavior here.
	got := classification.Decide("Wednesday transactions", "", buildMatcher(t))
	if got.Related {
		t.Error("Related=true, want false: Decide alone must not special-case any source, that's the caller's job")
	}
}

func TestDecide_EntitiesDetectedListsUniqueCanonicalNamesSorted(t *testing.T) {
	got := classification.Decide("Ezo Quarrington and Ezo again, plus Dan Pellingham", "", buildMatcher(t))
	want := []string{"Dan Pellingham", "Ezo Quarrington"}
	if len(got.EntitiesDetected) != len(want) {
		t.Fatalf("EntitiesDetected = %v, want %v", got.EntitiesDetected, want)
	}
	for i, w := range want {
		if got.EntitiesDetected[i] != w {
			t.Errorf("EntitiesDetected[%d] = %q, want %q", i, got.EntitiesDetected[i], w)
		}
	}
}

func TestDecide_DictionaryVersionIsCarriedFromMatcher(t *testing.T) {
	got := classification.Decide("Springfield Isotopes news", "", buildMatcher(t))
	if got.DictionaryVersion != "test-v1" {
		t.Errorf("DictionaryVersion = %q, want test-v1", got.DictionaryVersion)
	}
}
