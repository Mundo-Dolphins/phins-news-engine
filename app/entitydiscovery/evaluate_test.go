package entitydiscovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

type fakeAssessor struct {
	by    map[string]entitydiscovery.Assessment
	calls int
}

func (f *fakeAssessor) Assess(_ context.Context, c entitydiscovery.VerifyCandidate) entitydiscovery.Assessment {
	f.calls++
	if a, ok := f.by[c.Name]; ok {
		return a
	}
	return entitydiscovery.Assessment{Status: "not_found"}
}

func decisive(status string, md map[string]string) entitydiscovery.Assessment {
	return entitydiscovery.Assessment{
		Status: status, Candidates: 1, Decisive: true,
		Result: entitydiscovery.VerifyResult{
			Resolved: true, Metadata: md,
			Provenance: entitydiscovery.Provenance{Provider: "wikidata", ProviderID: "Q9"},
		},
	}
}

func evalRow(name string, et entityproposal.EntityType, kind entityproposal.Kind, st entityproposal.Status) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: entityproposal.Normalize(name), CanonicalName: name, NormalizedName: entityproposal.Normalize(name),
		EntityType: et, Kind: kind, Status: st,
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: name}},
	}
}

func evalFixture() (*fakeProposalRepo, *fakeAssessor) {
	repo := newFakeProposalRepo()
	repo.upserted = []entityproposal.Proposal{
		evalRow("Patrick Ostrander", entityproposal.EntityTypePlayer, entityproposal.KindNewEntity, entityproposal.StatusPending),
		evalRow("Brian Fennow", entityproposal.EntityTypeCoach, entityproposal.KindNewEntity, entityproposal.StatusPending),
		evalRow("Springfield Mackerels", entityproposal.EntityTypeTeam, entityproposal.KindNewEntity, entityproposal.StatusPending),
		evalRow("Springfield", entityproposal.EntityTypeTeam, entityproposal.KindPossibleDuplicate, entityproposal.StatusPending),
		evalRow("Zzyzx Qwerty", entityproposal.EntityTypePlayer, entityproposal.KindNewEntity, entityproposal.StatusPending),
		evalRow("Boom Error", entityproposal.EntityTypePlayer, entityproposal.KindNewAlias, entityproposal.StatusPending),
		evalRow("Manual Person", entityproposal.EntityTypePlayer, entityproposal.KindNewEntity, entityproposal.StatusApproved),
	}
	return repo, &fakeAssessor{by: map[string]entitydiscovery.Assessment{
		"Patrick Ostrander":     decisive("verified_nfl", map[string]string{"league": "NFL", "team": "Capital City Capitals", "position": "QB"}),
		"Brian Fennow":          decisive("verified_nfl", map[string]string{"league": "NFL"}),
		"Springfield Mackerels": decisive("verified_non_nfl", map[string]string{"league": "MLB"}),
		"Springfield":           {Status: "ambiguous", Candidates: 3},
		"Boom Error":            {Status: entitydiscovery.AssessmentError},
	}}
}

func TestEvaluate_CountsAndBreakdowns(t *testing.T) {
	repo, a := evalFixture()
	rep, err := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	want := entitydiscovery.Counts{
		Evaluated: 6, SearchHits: 4, VerifiedNFL: 2, VerifiedNonNFL: 1, Ambiguous: 1, NotFound: 1, Errors: 1,
		TeamMetadata: 1, PositionMetadata: 1, FallbackRequired: 3, ManualAvoided: 3,
	}
	if rep.Totals != want {
		t.Fatalf("totals = %+v, want %+v", rep.Totals, want)
	}
	if rep.Rates.ResolverCoverage != 0.5 || rep.Rates.TeamCoverage != 0.5 || rep.Rates.RemainingLLMRate != 0.5 {
		t.Fatalf("rates = %+v", rep.Rates)
	}
	var sumType, sumKind int
	for _, c := range rep.ByType {
		sumType += c.Evaluated
	}
	for _, c := range rep.ByKind {
		sumKind += c.Evaluated
	}
	if sumType != 6 || sumKind != 6 || rep.ByKind["possible_duplicate"].Ambiguous != 1 || rep.ByType["coach"].VerifiedNFL != 1 {
		t.Fatalf("byType=%+v byKind=%+v", rep.ByType, rep.ByKind)
	}
	if len(rep.Cases) != 6 {
		t.Fatalf("cases = %d", len(rep.Cases))
	}
}

func TestEvaluate_NeverMutatesProposals(t *testing.T) {
	repo, a := evalFixture()
	before := append([]entityproposal.Proposal(nil), repo.upserted...)
	if _, err := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, repo.upserted) || repo.updates != 0 {
		t.Fatalf("proposals mutated: updates=%d", repo.updates)
	}
}

func TestEvaluate_KindTypeStatusFilters(t *testing.T) {
	repo, a := evalFixture()
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Status: "pending", Kind: "new_alias"})
	if rep.Totals.Evaluated != 1 || rep.Totals.Errors != 1 {
		t.Fatalf("kind filter totals = %+v", rep.Totals)
	}
	rep, _ = entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Status: "approved"})
	if rep.Totals.Evaluated != 1 {
		t.Fatalf("status filter totals = %+v", rep.Totals)
	}
	rep, _ = entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Status: "pending", EntityType: "team"})
	if rep.Totals.Evaluated != 2 {
		t.Fatalf("type filter totals = %+v", rep.Totals)
	}
}

func TestEvaluate_LimitSampleDeterministicAndOversized(t *testing.T) {
	repo, a := evalFixture()
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Limit: 2})
	if rep.Totals.Evaluated != 2 {
		t.Fatalf("limit: %+v", rep.Totals)
	}
	ids := func(opts entitydiscovery.EvalOptions) string {
		r, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), opts)
		var s []string
		for _, c := range r.Cases {
			s = append(s, c.ProposalID)
		}
		return strings.Join(s, ",")
	}
	one := ids(entitydiscovery.EvalOptions{Sample: 3, Seed: 7})
	two := ids(entitydiscovery.EvalOptions{Sample: 3, Seed: 7})
	if one != two || len(strings.Split(one, ",")) != 3 {
		t.Fatalf("sample not deterministic: %q vs %q", one, two)
	}
	rep, err := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Sample: 500, Limit: 900})
	if err != nil || rep.Totals.Evaluated != 7 {
		t.Fatalf("oversized sample: %+v err=%v", rep.Totals, err)
	}
}

func TestEvaluate_LocalCatalogHitSkipsAssessor(t *testing.T) {
	repo, a := evalFixture()
	catalog := entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{{
		ID: "spr", CanonicalName: "Springfield Mackerels", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Mackerels"},
	}})
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, catalog, entitydiscovery.EvalOptions{Status: "pending"})
	if rep.Totals.LocalCatalog != 1 || a.calls != 5 || rep.Totals.FallbackRequired != 3 {
		t.Fatalf("totals=%+v calls=%d", rep.Totals, a.calls)
	}
}

func TestEvaluate_EmptyBacklogHasZeroRates(t *testing.T) {
	rep, err := entitydiscovery.Evaluate(context.Background(), newFakeProposalRepo(), &fakeAssessor{}, emptyCatalog(), entitydiscovery.EvalOptions{})
	if err != nil || rep.Totals.Evaluated != 0 || rep.Rates != (entitydiscovery.Rates{}) {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestEvaluate_ReviewedFalseResolutionRate(t *testing.T) {
	repo, a := evalFixture()
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{
		Status: "pending",
		Reviewed: map[string]string{
			"patrick ostrander": "correct", "springfield mackerels": "wrong",
			"springfield": "wrong", // ambiguous: not a resolution, ignored
			"unknown id":  "wrong",
		},
	})
	if rep.Review.Reviewed != 2 || rep.Review.Wrong != 1 || rep.Review.FalseResolutionRate == nil || *rep.Review.FalseResolutionRate != 0.5 {
		t.Fatalf("review = %+v", rep.Review)
	}
}

func TestEvalReport_TextAndJSONTotalsAgree(t *testing.T) {
	repo, a := evalFixture()
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{Status: "pending"})
	rep.CacheHits = 4
	var txt, js bytes.Buffer
	if err := rep.WriteText(&txt); err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	var back entitydiscovery.EvalReport
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Totals != rep.Totals || back.CacheHits != 4 {
		t.Fatalf("json totals = %+v", back.Totals)
	}
	for _, line := range []string{
		fmt.Sprintf("proposals evaluated: %d", rep.Totals.Evaluated),
		fmt.Sprintf("verified NFL: %d", rep.Totals.VerifiedNFL),
		fmt.Sprintf("verified non-NFL: %d", rep.Totals.VerifiedNonNFL),
		fmt.Sprintf("ambiguous: %d", rep.Totals.Ambiguous),
		fmt.Sprintf("not found: %d", rep.Totals.NotFound),
		fmt.Sprintf("provider errors: %d", rep.Totals.Errors),
		fmt.Sprintf("LLM fallback required: %d", rep.Totals.FallbackRequired),
		fmt.Sprintf("estimated manual reviews avoided: %d", rep.Totals.ManualAvoided),
		fmt.Sprintf("wikidata cache hits: %d", rep.CacheHits),
	} {
		if !strings.Contains(txt.String(), line) {
			t.Errorf("text report missing %q:\n%s", line, txt.String())
		}
	}
}

func TestEvaluate_ReviewedUnmatchedIsReportedNotZeroPercent(t *testing.T) {
	repo, a := evalFixture()
	rep, _ := entitydiscovery.Evaluate(context.Background(), repo, a, emptyCatalog(), entitydiscovery.EvalOptions{
		Status: "pending", Reviewed: map[string]string{"old id": "wrong", "springfield": "wrong"},
	})
	if rep.Review.Provided != 2 || rep.Review.Reviewed != 0 || rep.Review.Unmatched != 2 || rep.Review.FalseResolutionRate != nil {
		t.Fatalf("review = %+v", rep.Review)
	}
	var txt bytes.Buffer
	_ = rep.WriteText(&txt)
	if !strings.Contains(txt.String(), "2 of 2 reviewed ids matched no decisive case") || !strings.Contains(txt.String(), "n/a") {
		t.Fatalf("text:\n%s", txt.String())
	}
	var js bytes.Buffer
	_ = rep.WriteJSON(&js)
	if strings.Contains(js.String(), `"false_resolution_rate": 0`) {
		t.Fatalf("json reports a false 0%%:\n%s", js.String())
	}
}
