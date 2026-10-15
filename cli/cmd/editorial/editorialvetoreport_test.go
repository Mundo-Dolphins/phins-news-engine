package editorial

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

func TestEvaluateVetoImpactCountsEffectiveRulesAndPreservesLegitimateCoverage(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "animal-1", Title: "Isotopes spotted near the coast"},
		{ArticleID: "nfl-1", Title: "Isotopes spotted at NFL practice"},
		{ArticleID: "promo-1", Title: "Free bets before kickoff"},
	}
	cfg := editorialveto.Config{Rules: []editorialveto.Rule{
		{ReasonCode: "veto:noise:animal_isotope", Patterns: []string{"isotopes spotted"}, ExcludePatterns: []string{"NFL"}},
		{ReasonCode: "veto:gambling:sportsbook_promo", Patterns: []string{"free bets"}},
	}}
	report := evaluateVetoImpact(examples, cfg, "fixture.yml")
	if report.Blocked != 2 || report.ByReason["veto:noise:animal_isotope"] != 1 || report.ByReason["veto:gambling:sportsbook_promo"] != 1 {
		t.Fatalf("report = %+v, want two effective blocks split by reason", report)
	}
	if len(report.Examples["veto:noise:animal_isotope"]) != 1 || report.Examples["veto:noise:animal_isotope"][0] != "animal-1" {
		t.Fatalf("examples = %+v, want only animal-1", report.Examples)
	}
}
