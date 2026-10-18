package storyguard

import (
	"regexp"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

var textCfg = normalization.TextConfig{
	RepeatedPrefixPatterns: []string{`^\s*\[[^\]]*\]\s*`},
	StripEmojis:            true, StripHashtags: true, StripMentions: true, StripInlineURLs: true,
	ShingleSize: 3, MinTokenLength: 3,
	StopWords: []string{"the", "and", "with", "for", "from", "that", "this", "into", "over", "after", "before"},
}

func tokens(title string) []string { return normalization.Normalize(title, textCfg).Tokens }

var policy = Policy{Enabled: true, MaxPerStory: 2, MinSharedTokens: 4, OverlapThreshold: 0.6}

func TestSameStory(t *testing.T) {
	same := [][2]string{
		{"[NBC Sports] - Montgomery Burns sells sliver of Isotopes at record valuation", "BREAKING NEWS: Montgomery Burns sells 2.1% of the Isotopes at Over $12.5 Billion Valuation"},
		{"Kent Brockman No Longer Officially Working With Isotopes", "Kent Brockman is no longer officially working with the #Isotopes. Can consult or take a front office job"},
		{"[Springfield Gazette: Isotopes Wire] - Recently released Isotopes RB signs NFC South team", "Recently released Isotopes RB signs NFC South team 📸 Julio Aguilar, Getty Images"},
	}
	for _, pair := range same {
		if !policy.SameStory(tokens(pair[0]), tokens(pair[1])) {
			t.Errorf("expected same story:\n %q\n %q", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{"Montgomery Burns sells 2.1% of the Isotopes at record valuation", "Kent Brockman no longer officially working with Isotopes"},
		{"Isotopes injury report: Homer Simpson limited in practice", "Isotopes injury report: Ned Flanders did not practice Wednesday"},
		{"Isotopes bench Bart Simpson", "Bart Simpson checkdowns?"},
	}
	for _, pair := range different {
		if policy.SameStory(tokens(pair[0]), tokens(pair[1])) {
			t.Errorf("expected different stories:\n %q\n %q", pair[0], pair[1])
		}
	}
}

func TestExceeded(t *testing.T) {
	recent := []Recent{
		{ArticleID: "a", Tokens: tokens("Montgomery Burns sells 2.1% of the Isotopes at record valuation")},
		{ArticleID: "b", Tokens: tokens("Isotopes owner Montgomery Burns sells stake at record NFL valuation")},
		{ArticleID: "c", Tokens: tokens("Bart Simpson bench decision looms")},
	}
	got, ids := policy.Exceeded(tokens("Burns sells piece of Isotopes at record valuation"), recent)
	if !got || len(ids) != 2 {
		t.Fatalf("Exceeded = %v %v, want true with 2 matches", got, ids)
	}
	one := recent[:1]
	if got, _ := policy.Exceeded(tokens("Burns sells piece of Isotopes at record valuation"), one); got {
		t.Fatal("one prior message must still allow a second")
	}
	if got, _ := (Policy{}).Exceeded(tokens("Burns sells piece of Isotopes at record valuation"), recent); got {
		t.Fatal("disabled policy must never withhold")
	}
}

func TestExemptTitles(t *testing.T) {
	p := Policy{ExemptTitles: []*regexp.Regexp{regexp.MustCompile(`(?i)postgame press conference|meets with the media`)}}
	for title, want := range map[string]bool{
		"[NFL: Springfield Isotopes] - G/T Moe Szyslak - Oct 11 | Postgame Press Conference": true,
		"DL Lenny Leonard meets with the media | Springfield Isotopes":                       true,
		"Montgomery Burns sells 2.1% of the Isotopes":                                        false,
	} {
		if got := p.Exempt(title); got != want {
			t.Errorf("Exempt(%q) = %v, want %v", title, got, want)
		}
	}
}
