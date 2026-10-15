// domain/eventsignals/bench_test.go
package eventsignals_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func syntheticLeague(n int) []eventsignals.EntityRule {
	out := make([]eventsignals.EntityRule, 0, n)
	for i := 0; i < n; i++ {
		first, last := fmt.Sprintf("Firstname%d", i), fmt.Sprintf("Lastname%d", i)
		full := first + " " + last
		out = append(out, eventsignals.EntityRule{
			ID: fmt.Sprintf("league-%d", i), CanonicalName: full, Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague,
			Terms:            []string{full},
			ConditionalTerms: []eventsignals.ConditionalTerm{{Term: last, RequireAny: []string{"herons"}}},
			Prefilter:        eventsignals.PrefilterWords(full, last),
		})
	}
	return out
}

func benchArticle() article.Article {
	body := ""
	for i := 0; i < 60; i++ {
		body += "The Isotopes offense moved the ball against a tough defense while Ezo Quarrington looked for open receivers. "
	}
	return article.Article{ID: "b", Title: "Isotopes edge Sharks in a close game", Summary: "Springfield wins by a field goal.", Body: body, PublishedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
}

func benchExtract(b *testing.B, leagueRules int) {
	r := baseRules()
	r.Entities = append(r.Entities, syntheticLeague(leagueRules)...)
	a, now := benchArticle(), time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eventsignals.Extract(a, r, now)
	}
}

func BenchmarkExtract_NoLeagueRules(b *testing.B)   { benchExtract(b, 0) }
func BenchmarkExtract_3200LeagueRules(b *testing.B) { benchExtract(b, 3200) }
