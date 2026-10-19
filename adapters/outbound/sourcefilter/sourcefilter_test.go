package sourcefilter_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

func TestContainsBlockedKeyword(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		blocked []string
		want    bool
	}{
		{"match, case-insensitive", []string{"Isotopes SIGN new punter"}, []string{"sign"}, true},
		{"no match", []string{"Isotopes beat the Owls"}, []string{"injury report"}, false},
		{"empty blocked list", []string{"anything"}, nil, false},
		{"blank entries in blocked list ignored", []string{"anything"}, []string{"  ", ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcefilter.ContainsBlockedKeyword(tt.values, tt.blocked); got != tt.want {
				t.Errorf("ContainsBlockedKeyword() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainsRequiredKeyword(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		required []string
		want     bool
	}{
		{"empty required list always matches", []string{"anything"}, nil, true},
		{"match, case-insensitive", []string{"Isotopes news"}, []string{"isotopes"}, true},
		{"no match among required", []string{"Owls news"}, []string{"isotopes", "ezo"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcefilter.ContainsRequiredKeyword(tt.values, tt.required); got != tt.want {
				t.Errorf("ContainsRequiredKeyword() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainsBlockedURL(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		blocked []string
		want    bool
	}{
		{"match ignoring scheme and trailing slash", []string{"see https://spam.example/x"}, []string{"http://spam.example/"}, true},
		{"no match", []string{"https://good.example/x"}, []string{"spam.example"}, false},
		{"empty blocked list", []string{"https://anything"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcefilter.ContainsBlockedURL(tt.values, tt.blocked); got != tt.want {
				t.Errorf("ContainsBlockedURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSkippedAuthor(t *testing.T) {
	tests := []struct {
		name        string
		author      string
		skipAuthors []string
		want        bool
	}{
		{"exact match case-insensitive", "SomeBot", []string{"somebot"}, true},
		{"match ignoring leading @", "@somebot", []string{"somebot"}, true},
		{"no match", "realuser", []string{"somebot"}, false},
		{"empty author never skipped", "", []string{"somebot"}, false},
		{"empty skip list never skips", "somebot", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcefilter.IsSkippedAuthor(tt.author, tt.skipAuthors); got != tt.want {
				t.Errorf("IsSkippedAuthor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContentTypeFor(t *testing.T) {
	tests := []struct {
		sourceType string
		want       article.ContentType
	}{
		{"hard_news", article.ContentTypeArticle},
		{"social", article.ContentTypeSocialPost},
		{"analysis", article.ContentTypeVideo},
		{"community", article.ContentTypeUnknown},
		{"", article.ContentTypeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.sourceType, func(t *testing.T) {
			if got := sourcefilter.ContentTypeFor(tt.sourceType); got != tt.want {
				t.Errorf("ContentTypeFor(%q) = %q, want %q", tt.sourceType, got, tt.want)
			}
		})
	}
}

func TestContainsBlockedKeywordUnless(t *testing.T) {
	blocked := []string{"Hurricanes", "Canes"}
	keep := []string{"NFL scouts", "scouts from"}
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"off-topic is blocked", "Canes tight end earns Week 6 honors", true},
		{"NFL scouts at a game between blocked teams are kept", "NFL scouts in attendance at Hurricanes vs Seminoles: #Isotopes (x2)", false},
		{"keep match is case-insensitive", "Scouts FROM 12 teams at the Canes game", false},
		{"no blocked keyword at all", "Isotopes sign a punter", false},
	}
	for _, tc := range tests {
		if got := sourcefilter.ContainsBlockedKeywordUnless([]string{tc.text}, blocked, keep); got != tc.want {
			t.Errorf("%s: blocked = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !sourcefilter.ContainsBlockedKeywordUnless([]string{"Canes tight end earns honors"}, blocked, nil) {
		t.Error("no keep keywords: behaves like ContainsBlockedKeyword")
	}
}
