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
		{"[NBC Sports] - Stephen Ross sells sliver of Dolphins at record valuation", "BREAKING NEWS: Stephen Ross sells 2.1% of the Dolphins at Over $12.5 Billion Valuation"},
		{"Troy Aikman No Longer Officially Working With Dolphins", "Troy Aikman is no longer officially working with the #Dolphins. Can consult or take a front office job"},
		{"[USA Today: Dolphins Wire] - Recently released Dolphins RB signs NFC South team", "Recently released Dolphins RB signs NFC South team 📸 Julio Aguilar, Getty Images"},
	}
	for _, pair := range same {
		if !policy.SameStory(tokens(pair[0]), tokens(pair[1])) {
			t.Errorf("expected same story:\n %q\n %q", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{"Stephen Ross sells 2.1% of the Dolphins at record valuation", "Troy Aikman no longer officially working with Dolphins"},
		{"Dolphins injury report: Tua Tagovailoa limited in practice", "Dolphins injury report: Jaylen Waddle did not practice Wednesday"},
		{"Dolphins bench Malik Willis", "Malik Willis checkdowns?"},
	}
	for _, pair := range different {
		if policy.SameStory(tokens(pair[0]), tokens(pair[1])) {
			t.Errorf("expected different stories:\n %q\n %q", pair[0], pair[1])
		}
	}
}

func TestExceeded(t *testing.T) {
	recent := []Recent{
		{ArticleID: "a", Tokens: tokens("Stephen Ross sells 2.1% of the Dolphins at record valuation")},
		{ArticleID: "b", Tokens: tokens("Dolphins owner Stephen Ross sells stake at record NFL valuation")},
		{ArticleID: "c", Tokens: tokens("Malik Willis bench decision looms")},
	}
	got, ids := policy.Exceeded(tokens("Ross sells piece of Dolphins at record valuation"), recent)
	if !got || len(ids) != 2 {
		t.Fatalf("Exceeded = %v %v, want true with 2 matches", got, ids)
	}
	one := recent[:1]
	if got, _ := policy.Exceeded(tokens("Ross sells piece of Dolphins at record valuation"), one); got {
		t.Fatal("one prior message must still allow a second")
	}
	if got, _ := (Policy{}).Exceeded(tokens("Ross sells piece of Dolphins at record valuation"), recent); got {
		t.Fatal("disabled policy must never withhold")
	}
}

func TestExemptTitles(t *testing.T) {
	p := Policy{ExemptTitles: []*regexp.Regexp{regexp.MustCompile(`(?i)postgame press conference|meets with the media`)}}
	for title, want := range map[string]bool{
		"[NFL: Miami Dolphins] - G/T Kadyn Proctor - Oct 11 | Postgame Press Conference": true,
		"DL Kenneth Grant meets with the media | Miami Dolphins":                         true,
		"Stephen Ross sells 2.1% of the Dolphins":                                        false,
	} {
		if got := p.Exempt(title); got != want {
			t.Errorf("Exempt(%q) = %v, want %v", title, got, want)
		}
	}
}
