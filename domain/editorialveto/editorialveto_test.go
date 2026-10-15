package editorialveto_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

func testConfig() editorialveto.Config {
	return editorialveto.Config{
		Version: "test-v1",
		Rules: []editorialveto.Rule{
			{Category: editorialveto.CategoryGambling, ReasonCode: "veto:gambling:sportsbook_promo", Patterns: []string{"free bets", "odds boost"}},
			{Category: editorialveto.CategorySpam, ReasonCode: "veto:spam:generic", Patterns: []string{"click here now", "act immediately"}},
			{Category: editorialveto.CategoryNoise, ReasonCode: "veto:noise:excess_punctuation", Patterns: []string{`[!?]{3,}`}},
		},
	}
}

func TestEvaluate_TriggersOnMatchingPhraseWithReasonCode(t *testing.T) {
	result := editorialveto.Evaluate("Get your free bets before kickoff!", testConfig())
	if !result.Triggered {
		t.Fatal("Triggered = false, want true")
	}
	if result.ReasonCode != "veto:gambling:sportsbook_promo" {
		t.Errorf("ReasonCode = %q, want %q", result.ReasonCode, "veto:gambling:sportsbook_promo")
	}
	if result.Category != editorialveto.CategoryGambling {
		t.Errorf("Category = %q, want %q", result.Category, editorialveto.CategoryGambling)
	}
	if !reflect.DeepEqual(result.MatchedTerms, []string{"free bets"}) {
		t.Errorf("MatchedTerms = %v, want [free bets]", result.MatchedTerms)
	}
}

func TestEvaluate_NoMatchIsNotTriggered(t *testing.T) {
	result := editorialveto.Evaluate("Isotopes sign veteran cornerback to two-year extension", testConfig())
	if result.Triggered {
		t.Fatalf("Triggered = true (reason %q), want false", result.ReasonCode)
	}
	if result.ReasonCode != "" {
		t.Errorf("ReasonCode = %q, want empty", result.ReasonCode)
	}
}

func TestEvaluate_WordBoundaryPreventsSubstringFalsePositive(t *testing.T) {
	// "act immediately" must not match inside an unrelated longer phrase
	// that merely shares a substring — this exercises phrase-boundary
	// matching, mirroring domain/editorial/lexicon.go's existing care.
	result := editorialveto.Evaluate("The team will react immediately to the injury news", testConfig())
	if result.Triggered {
		t.Fatalf("Triggered = true (reason %q) on a substring match, want false", result.ReasonCode)
	}
}

func TestEvaluate_RegexPatternMatchesForNoiseCategory(t *testing.T) {
	result := editorialveto.Evaluate("This is shocking news!!! You won't believe it!!!", testConfig())
	if !result.Triggered || result.Category != editorialveto.CategoryNoise {
		t.Fatalf("Triggered=%v Category=%q, want triggered/noise", result.Triggered, result.Category)
	}
}

func TestEvaluate_FirstMatchingRuleWinsWhenMultipleRulesMatch(t *testing.T) {
	// A text matching both a gambling phrase and a noise pattern reports
	// whichever rule is declared first in cfg.Rules — deterministic,
	// order-of-config-defined precedence, not "the first alphabetically"
	// or any other implicit rule.
	result := editorialveto.Evaluate("Free bets available now!!!", testConfig())
	if result.Category != editorialveto.CategoryGambling {
		t.Errorf("Category = %q, want %q (first rule in config order)", result.Category, editorialveto.CategoryGambling)
	}
}

func TestEvaluate_EmptyConfigNeverTriggers(t *testing.T) {
	result := editorialveto.Evaluate("Anything at all, free bets included", editorialveto.Config{})
	if result.Triggered {
		t.Fatal("Triggered = true with an empty Config, want false")
	}
}

func TestEvaluate_CaseSensitiveRegexPatternsPreserveCase(t *testing.T) {
	// Regex patterns (those containing metacharacters like [A-Z]) should be
	// case-sensitive. A character-class pattern [A-Z]{12,} (matching 12+ uppercase
	// letters) should match genuinely all-uppercase text but NOT match lowercase
	// or mixed-case text of the same length. This is required for patterns like
	// [A-Z]{12,} to correctly identify ALL-CAPS spam/noise.
	cfg := editorialveto.Config{
		Version: "test-v1",
		Rules: []editorialveto.Rule{
			{Category: editorialveto.CategoryNoise, ReasonCode: "veto:noise:all_caps", Patterns: []string{`[A-Z]{12,}`}},
		},
	}

	// Test 1: All-uppercase text should trigger (ALLCAPSTEXTHERE = 14 chars)
	result := editorialveto.Evaluate("This is ALLCAPSTEXTHERE in the middle", cfg)
	if !result.Triggered {
		t.Fatalf("Triggered = false on all-caps substring, want true")
	}
	if result.Category != editorialveto.CategoryNoise {
		t.Errorf("Category = %q, want %q", result.Category, editorialveto.CategoryNoise)
	}

	// Test 2: Lowercase text of the same length should NOT trigger
	result = editorialveto.Evaluate("This is allcapstexthere in the middle", cfg)
	if result.Triggered {
		t.Fatalf("Triggered = true on lowercase text (same length), want false (case-sensitive)")
	}

	// Test 3: Mixed-case text should NOT trigger
	result = editorialveto.Evaluate("This is AllCapsTextHere in the middle", cfg)
	if result.Triggered {
		t.Fatalf("Triggered = true on mixed-case text, want false (case-sensitive)")
	}
}

func TestEvaluate_ExcludePatternPreservesNFLCoverage(t *testing.T) {
	cfg := editorialveto.Config{Rules: []editorialveto.Rule{{
		Category: editorialveto.CategoryNoise, ReasonCode: "veto:noise:animal_isotope",
		Patterns: []string{"isotopes spotted"}, ExcludePatterns: []string{"NFL", "football"},
	}}}
	if got := editorialveto.Evaluate("Isotopes spotted near the coast", cfg); !got.Triggered {
		t.Fatal("animal rule did not trigger")
	}
	if got := editorialveto.Evaluate("Isotopes spotted at NFL practice", cfg); got.Triggered {
		t.Fatalf("animal rule triggered for legitimate NFL coverage: %+v", got)
	}
}

func TestEvaluateAllReportsEveryMatchingRule(t *testing.T) {
	cfg := editorialveto.Config{Rules: []editorialveto.Rule{
		{Category: editorialveto.CategoryGambling, ReasonCode: "veto:gambling:x", Patterns: []string{"free bets"}},
		{Category: editorialveto.CategorySpam, ReasonCode: "veto:spam:y", Patterns: []string{"click here"}},
	}}
	got := editorialveto.EvaluateAll("Free bets — click here", cfg)
	if len(got) != 2 || got[0].ReasonCode != "veto:gambling:x" || got[1].ReasonCode != "veto:spam:y" {
		t.Fatalf("EvaluateAll() = %+v, want both matching rules in order", got)
	}
}
