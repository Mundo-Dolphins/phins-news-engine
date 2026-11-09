package entities

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestEntitiesCommand_RegistersWikidataEvaluate(t *testing.T) {
	found, _, err := NewCommand().Find([]string{"wikidata", "evaluate"})
	if err != nil || found == nil || found.Name() != "evaluate" {
		t.Fatalf("Find = %v, %v", found, err)
	}
}

type staticAssessor struct{}

func (staticAssessor) Assess(context.Context, entitydiscovery.VerifyCandidate) entitydiscovery.Assessment {
	return entitydiscovery.Assessment{
		Status: "verified_non_nfl", Decisive: true, Candidates: 1,
		Result: entitydiscovery.VerifyResult{Resolved: true},
	}
}

// Real SQLite: evaluating must leave every stored proposal identical.
func TestEvaluate_RealSQLite_DoesNotMutate(t *testing.T) {
	_, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	for _, n := range []string{"Springfield Mackerels", "Patrick Ostrander"} {
		if _, err := repo.Upsert(ctx, entityproposal.Proposal{
			CanonicalName: n, NormalizedName: entityproposal.Normalize(n),
			EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity,
			Confidence: 0.6, Occurrences: 1, ExtractorVersion: "t", CreatedAt: time.Now().UTC(),
			Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: n}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := repo.List(ctx, "", "")
	rep, err := entitydiscovery.Evaluate(ctx, repo, staticAssessor{}, entitydiscovery.BuildCatalogIndex(nil), entitydiscovery.EvalOptions{Status: "pending"})
	if err != nil || rep.Totals.VerifiedNonNFL != 2 {
		t.Fatalf("rep=%+v err=%v", rep.Totals, err)
	}
	after, _ := repo.List(ctx, "", "")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("proposals changed:\nbefore=%+v\nafter=%+v", before, after)
	}
	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil || buf.Len() == 0 {
		t.Fatalf("text render: %v", err)
	}
}

func TestWikidataEvaluateCommand_RejectsUnknownFormat(t *testing.T) {
	cmd := newWikidataEvaluateCommand()
	cmd.SetArgs([]string{"--format", "yaml"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || err.Error() != `entities wikidata evaluate: --format must be text or json (got "yaml")` {
		t.Fatalf("Execute() error = %v, want --format validation error", err)
	}
}
