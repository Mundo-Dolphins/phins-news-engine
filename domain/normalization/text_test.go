package normalization_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

func defaultTextConfig() normalization.TextConfig {
	return normalization.TextConfig{
		StripEmojis:            true,
		StripHashtags:          true,
		StripMentions:          true,
		StripInlineURLs:        true,
		RepeatedPrefixPatterns: []string{`^\s*\[[^\]]*\]\s*`},
		ShingleSize:            3,
		MinTokenLength:         3,
		StopWords:              []string{"the", "and", "with"},
	}
}

func TestNormalize_PreservesOriginal(t *testing.T) {
	got := normalization.Normalize("  Isotopes sign new WR  ", defaultTextConfig())
	if got.Original != "  Isotopes sign new WR  " {
		t.Errorf("Original = %q, want input preserved verbatim", got.Original)
	}
}

func TestNormalize_ForClassificationDecodesEntitiesAndCollapsesSpace(t *testing.T) {
	got := normalization.Normalize("Isotopes  &amp;   Sharks  preview", defaultTextConfig())
	if got.ForClassification != "Isotopes & Sharks preview" {
		t.Errorf("ForClassification = %q, want decoded entities and collapsed whitespace", got.ForClassification)
	}
}

func TestNormalize_ForDedupStripsBracketPrefixHashtagsMentionsURLsAndLowercases(t *testing.T) {
	got := normalization.Normalize(
		"[BREAKING] Isotopes beat @Sharks 24-17 #NFL see https://example.com/box-score",
		defaultTextConfig(),
	)
	want := "isotopes beat sharks 24-17 see"
	if got.ForDedup != want {
		t.Errorf("ForDedup = %q, want %q", got.ForDedup, want)
	}
}

func TestNormalize_ForDedupExpandsCommonAbbreviations(t *testing.T) {
	got := normalization.Normalize("Isotopes vs. Sharks", defaultTextConfig())
	if got.ForDedup != "isotopes versus sharks" {
		t.Errorf("ForDedup = %q, want vs. expanded to versus", got.ForDedup)
	}
}

func TestNormalize_TokensFilteredByStopWordsAndMinLength(t *testing.T) {
	got := normalization.Normalize("The Isotopes and the Sharks", defaultTextConfig())
	want := []string{"isotopes", "sharks"}
	if !reflect.DeepEqual(got.Tokens, want) {
		t.Errorf("Tokens = %v, want %v (stop words and short tokens removed)", got.Tokens, want)
	}
}

func TestNormalize_ShinglesAreWordNGramsOfConfiguredSize(t *testing.T) {
	cfg := defaultTextConfig()
	cfg.ShingleSize = 2
	cfg.StopWords = nil
	cfg.MinTokenLength = 1
	got := normalization.Normalize("Isotopes beat Sharks today", cfg)
	want := []string{"isotopes beat", "beat sharks", "sharks today"}
	if !reflect.DeepEqual(got.Shingles, want) {
		t.Errorf("Shingles = %v, want %v", got.Shingles, want)
	}
}

func TestNormalize_ShinglesEmptyWhenFewerTokensThanShingleSize(t *testing.T) {
	got := normalization.Normalize("Isotopes", defaultTextConfig())
	if len(got.Shingles) != 0 {
		t.Errorf("Shingles = %v, want empty (only 1 token, shingle size 3)", got.Shingles)
	}
}

func TestNormalize_DisablingStripFlagsKeepsContent(t *testing.T) {
	cfg := defaultTextConfig()
	cfg.StripHashtags = false
	cfg.StripMentions = false
	cfg.StripInlineURLs = false
	got := normalization.Normalize("Isotopes @coach #NFL https://example.com", cfg)
	if got.ForDedup != "isotopes @coach #nfl https://example.com" {
		t.Errorf("ForDedup = %q, want mentions/hashtags/URLs kept when stripping disabled", got.ForDedup)
	}
}

func TestNormalize_BoilerplatePhrasesStrippedFromClassificationAndDedup(t *testing.T) {
	cfg := defaultTextConfig()
	cfg.BoilerplatePhrases = []string{"Subscribe to our channel.", "Follow us on Facebook."}
	got := normalization.Normalize(
		"Isotopes beat the Sharks. Subscribe to our channel. Follow us on Facebook.",
		cfg,
	)
	if got.ForClassification != "Isotopes beat the Sharks." {
		t.Errorf("ForClassification = %q, want boilerplate CTAs stripped", got.ForClassification)
	}
	for _, tok := range got.Tokens {
		if tok == "subscribe" || tok == "facebook" {
			t.Errorf("Tokens = %v, want boilerplate tokens excluded", got.Tokens)
		}
	}
}

func TestNormalize_NFCNormalizesComposedAndDecomposedFormsIdentically(t *testing.T) {
	// "café" as a single precomposed 'é' (U+00E9) vs. 'e' + combining
	// acute accent (U+0065 U+0301) must normalize to the same text —
	// otherwise two copies of a syndicated headline with different
	// Unicode encodings would be treated as different articles.
	composed := "Café con leche"
	decomposed := "Café con leche"

	got1 := normalization.Normalize(composed, defaultTextConfig())
	got2 := normalization.Normalize(decomposed, defaultTextConfig())
	if got1.ForClassification != got2.ForClassification {
		t.Errorf("ForClassification differs by Unicode form: %q vs %q", got1.ForClassification, got2.ForClassification)
	}
}

func TestNormalize_StripsGenericTransportDecorationsForDedup(t *testing.T) {
	cfg := normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3}
	clean := normalization.Normalize("Springfield Isotopes Fans: Isotopes Vs Cypress Comets Predictions", cfg)
	for _, raw := range []string{
		"[Fan Network: The Topes Report] - Springfield Isotopes Fans: Isotopes Vs Cypress Comets Predictions",
		"NEW: Springfield Isotopes Fans: Isotopes Vs Cypress Comets Predictions",
		"BREAKING - [Feed] Springfield Isotopes Fans: Isotopes Vs Cypress Comets Predictions",
	} {
		if got := normalization.Normalize(raw, cfg); got.ForDedup != clean.ForDedup {
			t.Errorf("Normalize(%q).ForDedup = %q, want %q", raw, got.ForDedup, clean.ForDedup)
		}
	}
}

func TestNormalize_FlagsSocialTruncation(t *testing.T) {
	cfg := normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3}
	for _, raw := range []string{"Isotopes sign a new corner back…", "Isotopes sign a new corner back..."} {
		got := normalization.Normalize(raw, cfg)
		if !got.Truncated || got.ForDedup != "isotopes sign a new corner back" {
			t.Errorf("Normalize(%q) = truncated %v dedup %q", raw, got.Truncated, got.ForDedup)
		}
	}
	if normalization.Normalize("Isotopes sign a new corner back", cfg).Truncated {
		t.Error("untruncated text flagged as truncated")
	}
}
