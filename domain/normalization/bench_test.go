package normalization_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

// longArticleBodyFixture builds a ~500-word article body by repeating a
// realistic paragraph, so Normalize's per-call cost can be measured against
// input sizes closer to a real story body rather than a headline-sized string.
func longArticleBodyFixture() string {
	paragraph := "The Springfield Isotopes announced on Monday that they have agreed to terms " +
		"with a veteran wide receiver on a one-year deal, according to sources familiar " +
		"with the negotiations. The move comes as the team looks to bolster its receiving " +
		"corps ahead of the regular season opener against the Shelbyville Sharks. \"We're " +
		"excited to add a player of this caliber to our roster,\" the head coach said in " +
		"a statement released by the team's media relations department. The 29-year-old " +
		"receiver spent the last three seasons with a division rival, where he posted " +
		"back-to-back 1,000-yard seasons before an injury limited him to just eight games " +
		"last year. League sources indicate the contract includes performance incentives " +
		"that could push its value higher depending on games played and receiving yards. "

	var b strings.Builder
	for b.Len() < 3000 {
		b.WriteString(paragraph)
	}
	return b.String()
}

func BenchmarkNormalize_Title(b *testing.B) {
	cfg := defaultTextConfig()
	title := "[BREAKING] Isotopes sign veteran WR to one-year deal, sources say"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalization.Normalize(title, cfg)
	}
}

func BenchmarkNormalize_Body(b *testing.B) {
	cfg := defaultTextConfig()
	body := longArticleBodyFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalization.Normalize(body, cfg)
	}
}

func BenchmarkNormalizeURL(b *testing.B) {
	cfg := defaultURLConfig()
	raw := "https://isotopeswire.example.com/story/sports/nfl/isotopes/2026/08/03/isotopes-news-headline/?utm_source=twitter&utm_medium=social&fbclid=abc123"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = normalization.NormalizeURL(raw, cfg, nil)
	}
}
