// infra/eventrules/eventrules_test.go
package eventrules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// repoPath resolves a repository-relative path from the module root.
func repoPath(t *testing.T, rel string) string {
	t.Helper()
	return repofs.Path(t, filepath.FromSlash(rel))
}

func enabledConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Clustering.RulesPath = repoPath(t, "configs/example/event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repoPath(t, "configs/example/entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repoPath(t, "configs/example/entity_catalog_generated.yml")
	return &cfg
}

func TestGeneratedPath_ConfigOrConventionalDefault(t *testing.T) {
	var cfg config.Config
	if got := GeneratedPath(&cfg); got != "configs/event_rules_generated.yml" {
		t.Fatalf("GeneratedPath() = %q, want the conventional path while the feature is off", got)
	}
	custom := filepath.Join(t.TempDir(), "x.yml")
	cfg.Clustering.GeneratedRulesPath = custom
	if got := GeneratedPath(&cfg); got != custom {
		t.Fatalf("GeneratedPath() = %q, want the configured path", got)
	}
}

func TestReadApplied_MissingFileIsEmpty(t *testing.T) {
	got, err := ReadApplied(filepath.Join(t.TempDir(), "missing.yml"))
	if err != nil || len(got.Rules) != 0 || len(got.Types) != 0 {
		t.Fatalf("ReadApplied() = %v, %v; want empty, nil", got, err)
	}
}

func TestWriteAppliedReadApplied_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.yml")
	in := []eventrulecycle.AppliedRule{
		{TargetKind: "signal", Target: "rumor", Phrase: "lobbying for", ProposalID: "evr_2", PromptVersion: "event-rules-v1"},
		{TargetKind: "action", Target: "sign", Phrase: "puts pen to paper", ProposalID: "evr_1", PromptVersion: "event-rules-v1"},
		{TargetKind: "action", Target: "sign", Phrase: "inks a deal", ProposalID: "evr_3", PromptVersion: "event-rules-v1"},
	}
	types := []eventrulecycle.AppliedType{
		{Name: "kicking-error", Description: "A missed kick.", Referential: true, Window: "72h", ProposalID: "evr_t1", PromptVersion: "event-rules-v4"},
	}
	in = append(in, eventrulecycle.AppliedRule{TargetKind: "action", Target: "kicking-error", Phrase: "misses a kick", ProposalID: "evr_4", PromptVersion: "event-rules-v4"})
	if err := WriteApplied(path, eventrulecycle.AppliedSet{Rules: in, Types: types}); err != nil {
		t.Fatal(err)
	}
	g, err := clusteringrules.ParseGenerated(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.ActionTerms["sign"]) != 2 || len(g.ContentCues["rumor"]) != 1 || len(g.Types) != 1 || !reflect.DeepEqual(g.Types[0].Flags, []string{"referential"}) {
		t.Fatalf("written file = %+v; actions go to action_terms, signals to content_cues", g)
	}
	got, err := ReadApplied(path)
	if err != nil {
		t.Fatal(err)
	}
	want := eventrulecycle.AppliedSet{Rules: []eventrulecycle.AppliedRule{in[3], in[2], in[1], in[0]}, Types: types} // actions then signals, sorted
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadApplied() = %+v\nwant %+v", got, want)
	}
}

func TestWriteApplied_UnknownTargetKindIsAnError(t *testing.T) {
	err := WriteApplied(filepath.Join(t.TempDir(), "g.yml"), eventrulecycle.AppliedSet{Rules: []eventrulecycle.AppliedRule{{TargetKind: "mood", Target: "x", Phrase: "a b"}}})
	if err == nil {
		t.Fatal("an unknown target kind must not be written silently")
	}
}

func TestRunGate_MissingDatasetIsAWarningSentinel(t *testing.T) {
	cfg := enabledConfig(t)
	_, err := RunGate(context.Background(), cfg, filepath.Join(t.TempDir(), "nope.yml"), eventrulecycle.AppliedSet{})
	if !errors.Is(err, ErrGateDatasetMissing) {
		t.Fatalf("err = %v, want ErrGateDatasetMissing", err)
	}
}

func TestRunGate_InertCandidatePasses(t *testing.T) {
	cfg := enabledConfig(t)
	res, err := RunGate(context.Background(), cfg, repoPath(t, "testdata/clustering_groups.yml"), eventrulecycle.AppliedSet{Rules: []eventrulecycle.AppliedRule{
		{TargetKind: "action", Target: "sign", Phrase: "zzqx unmatched phrase", ProposalID: "evr_1", PromptVersion: "event-rules-v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Before.ArticlesTotal == 0 || !reflect.DeepEqual(res.Before.Pairwise, res.After.Pairwise) {
		t.Fatalf("gate = %+v; a phrase matching nothing must pass with identical metrics", res)
	}
	if res.Affected != 0 {
		t.Fatalf("Affected = %d, want 0 for a phrase matching nothing", res.Affected)
	}
}

// overMergeDataset is two gold groups whose articles are generic under the
// hand-written rules (never merged) and become the same transaction about
// the same player under the candidate phrase.
const overMergeDataset = `groups:
  - id: story-a
    articles:
      - id: x1
        title: "Riley Stubbs zzqx pens a pact"
        source_id: example-sports-wire
        source_type: rss
        domain: news.example.org
        content_type: article
        published_at: "2026-03-01T09:00:00Z"
  - id: story-b
    articles:
      - id: x2
        title: "Riley Stubbs zzqx pens a pact for charity"
        source_id: example-fan-blog
        source_type: rss
        domain: blog.example.net
        content_type: article
        published_at: "2026-03-01T10:00:00Z"
`

func TestRunGate_CandidateThatMergesDistinctStoriesIsBlocked(t *testing.T) {
	cfg := enabledConfig(t)
	dataset := filepath.Join(t.TempDir(), "groups.yml")
	if err := os.WriteFile(dataset, []byte(overMergeDataset), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := RunGate(context.Background(), cfg, dataset, eventrulecycle.AppliedSet{Rules: []eventrulecycle.AppliedRule{
		{TargetKind: "action", Target: "sign", Phrase: "zzqx pens a pact", ProposalID: "evr_1", PromptVersion: "event-rules-v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Reason == "" {
		t.Fatalf("gate = %+v; merging two gold stories must block", res)
	}
	if res.Affected != 2 {
		t.Fatalf("Affected = %d, want 2", res.Affected)
	}
}

// A candidate type reaches the gate's temp file: its phrase retypes the two
// gold articles (otherwise generic), so the gate sees them change.
func TestRunGate_CarriesTypes(t *testing.T) {
	cfg := enabledConfig(t)
	dataset := filepath.Join(t.TempDir(), "groups.yml")
	if err := os.WriteFile(dataset, []byte(overMergeDataset), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := RunGate(context.Background(), cfg, dataset, eventrulecycle.AppliedSet{
		Types: []eventrulecycle.AppliedType{{Name: "kicking-error", Description: "A missed kick.", Window: "48h", ProposalID: "evr_t1", PromptVersion: "event-rules-v4"}},
		Rules: []eventrulecycle.AppliedRule{{TargetKind: "action", Target: "kicking-error", Phrase: "zzqx pens a pact", ProposalID: "evr_1", PromptVersion: "event-rules-v4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Affected != 2 {
		t.Fatalf("Affected = %d, want 2", res.Affected)
	}
}

func TestDeployPaths_OnlyWhenRulesWereAppliedAndTheFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.yml")
	var cfg config.Config
	cfg.Clustering.GeneratedRulesPath = path
	if got := DeployPaths(&cfg, eventrulecycle.Result{Applied: 1}); len(got) != 0 {
		t.Fatalf("missing file: DeployPaths() = %v, want none", got)
	}
	if err := os.WriteFile(path, []byte("meta: {version: 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DeployPaths(&cfg, eventrulecycle.Result{}); len(got) != 0 {
		t.Fatalf("nothing applied: DeployPaths() = %v, want none", got)
	}
	if got := DeployPaths(&cfg, eventrulecycle.Result{Applied: 2}); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("DeployPaths() = %v, want [%s]", got, path)
	}
}
