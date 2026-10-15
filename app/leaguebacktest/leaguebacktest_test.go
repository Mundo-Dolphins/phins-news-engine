package leaguebacktest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguebacktest"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func rules() eventsignals.Rules {
	return eventsignals.Rules{
		FieldWeights: map[string]float64{"title": 1, "description": 0.8, "body": 0.4},
		Entities: []eventsignals.EntityRule{
			{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"isotopes"}, IsPrimaryTeam: true},
			{ID: "ezo", CanonicalName: "Ezo Quarrington", Kind: eventsignals.EntityKindPlayer, Terms: []string{"ezo quarrington", "ezo"}},
		},
	}
}

func league(name string, terms ...string) eventsignals.EntityRule {
	return eventsignals.EntityRule{ID: "league-" + eventsignals.Slug(name), CanonicalName: name, Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague, Terms: terms, Prefilter: eventsignals.PrefilterWords(terms...)}
}

func arts(n int, title string) []article.Article {
	out := make([]article.Article, n)
	for i := range out {
		out[i] = article.Article{ID: fmt.Sprintf("a%d", i), Title: title, Summary: "Isotopes news", PublishedAt: now}
	}
	return out
}

func TestRun_ScopedLeagueRulesPassTheInvariant(t *testing.T) {
	rep := leaguebacktest.Run(leaguebacktest.Config{
		Articles: append(arts(3, "Josh Wrenfield beats Isotopes"), arts(2, "Quiet day")...),
		Base:     rules(), League: []eventsignals.EntityRule{league("Josh Wrenfield", "josh wrenfield")}, Now: now,
	})
	if !rep.OK() || rep.Articles != 5 || rep.WithLeagueMention != 3 || rep.LeagueMentions != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestRun_AnUnscopedRuleThatChangesThePrimaryEntityIsReported(t *testing.T) {
	bad := league("Josh Wrenfield", "josh wrenfield")
	bad.Scope = "" // a Isotopes-scope rule would displace the primary entity
	rep := leaguebacktest.Run(leaguebacktest.Config{Articles: arts(2, "Josh Wrenfield beats Isotopes as Ezo watches"), Base: rules(), League: []eventsignals.EntityRule{bad}, Now: now})
	if rep.OK() || len(rep.InvariantViolations) != 2 {
		t.Fatalf("report = %+v; the harness must detect a rule that changes more than the allowed fields", rep)
	}
}

func TestRun_FlagsEntitiesMatchedInManyArticles(t *testing.T) {
	rep := leaguebacktest.Run(leaguebacktest.Config{
		Articles: arts(200, "Hexley was spotted near Isotopes camp"), Base: rules(),
		League: []eventsignals.EntityRule{league("Jordan Hexley", "hexley")}, Now: now, FrequencyPct: 0.5,
	})
	if len(rep.Suspicious) != 1 || rep.Suspicious[0].Name != "Jordan Hexley" || rep.Suspicious[0].Share < 0.99 || len(rep.Suspicious[0].SampleTitles) == 0 {
		t.Fatalf("Suspicious = %+v", rep.Suspicious)
	}
}

func TestRun_CountsUnknownEntitiesBeforeAndAfter(t *testing.T) {
	rep := leaguebacktest.Run(leaguebacktest.Config{Articles: arts(3, "Josh Wrenfield visits Springfield"), Base: rules(), League: []eventsignals.EntityRule{league("Josh Wrenfield", "josh wrenfield")}, Now: now})
	if rep.UnknownOn > rep.UnknownOff {
		t.Fatalf("unknown entities went up with league rules: off=%d on=%d", rep.UnknownOff, rep.UnknownOn)
	}
	if txt := rep.Text(); !strings.Contains(txt, "invariant: OK") || !strings.Contains(txt, "articles: 3") {
		t.Fatalf("Text() =\n%s", txt)
	}
}

func TestRun_NoArticlesIsAnOKEmptyReport(t *testing.T) {
	rep := leaguebacktest.Run(leaguebacktest.Config{Base: rules(), League: []eventsignals.EntityRule{league("Josh Wrenfield", "josh wrenfield")}, Now: now})
	if !rep.OK() || rep.Articles != 0 {
		t.Fatalf("report = %+v", rep)
	}
	_ = rep.Text() // must not divide by zero
}
