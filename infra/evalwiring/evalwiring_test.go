package evalwiring

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

const clusteringDataset = `
groups:
  - id: g1
    representative: a1
    articles:
      - id: a1
        title: "Isotopes Sign Veteran Linebacker"
        source_id: src-1
        content_type: article
        published_at: "2026-07-01T10:00:00Z"
        expected_role: first_report
      - id: a2
        title: "Isotopes Signed Veteran Linebacker, Outlet Reports"
        source_id: src-2
        content_type: article
        published_at: "2026-07-01T11:00:00Z"
        expected_role: confirmation
`

const dedupDataset = `
pairs:
  - a: {title: "Isotopes Beat Sharks In Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Isotopes Beat Sharks In An Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:05:00Z"}
    label: same_content
  - a: {title: "Isotopes Beat Sharks In Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Weather Forecast Calls For Rain This Weekend", source_type: rss, content_type: article, published_at: "2026-07-01T08:00:00Z"}
    label: different
  - a: {title: "Isotopes Sign Veteran Linebacker", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Isotopes Sign Linebacker", source_type: rss, content_type: article, published_at: "2026-07-01T10:05:00Z"}
    label: ambiguous
`

func exampleConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Clustering.RulesPath = repofs.Path(t, "configs", "example", "event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repofs.Path(t, "configs", "example", "entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repofs.Path(t, "configs", "example", "entity_catalog_generated.yml")
	return &cfg
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const badGeneratedRules = "meta: {version: 1}\naction_terms:\n  teleport:\n    - {phrase: zzqx, proposal_id: a, prompt_version: v}\n"

func TestClusteringOverridesGeneratedRules(t *testing.T) {
	cfg := exampleConfig(t)
	dataset := writeFile(t, "groups.yml", clusteringDataset)
	cfg.Clustering.GeneratedRulesPath = writeFile(t, "configured.yml", badGeneratedRules)

	// The configured (malformed) file is used when no override is given.
	if _, _, err := Clustering(cfg, dataset, ""); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("Clustering() with the configured bad file error = %v, want the unknown action reported", err)
	}

	// An explicit override replaces it, so the same config now evaluates.
	override := writeFile(t, "override.yml", "meta: {version: 1}\n")
	report, warnings, err := Clustering(cfg, dataset, override)
	if err != nil {
		t.Fatalf("Clustering() with an override error = %v", err)
	}
	if report.ArticlesTotal != 2 {
		t.Errorf("ArticlesTotal = %d, want 2", report.ArticlesTotal)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

func TestDedupEvaluatedExcludesAmbiguous(t *testing.T) {
	var cfg config.Config
	nd := &cfg.Deduplication.NearDuplicate
	nd.Method = "jaccard"
	nd.DefaultThreshold = 0.82
	nd.ShortTextMaxTokens = 8
	nd.ShortTextThreshold = 0.90
	cfg.Clustering.EventThreshold = 0.55
	report, evaluated, err := Dedup(&cfg, writeFile(t, "pairs.yml", dedupDataset))
	if err != nil {
		t.Fatalf("Dedup() error = %v", err)
	}
	if evaluated != 2 || report.Excluded != 1 {
		t.Errorf("evaluated = %d, excluded = %d, want 2 and 1", evaluated, report.Excluded)
	}
}

func TestLeagueDaysMustBePositive(t *testing.T) {
	_, err := League(context.Background(), (*sql.DB)(nil), exampleConfig(t), LeagueOptions{Days: 0}, time.Now())
	if err == nil || err.Error() != "--days must be positive" {
		t.Fatalf("League() error = %v, want %q", err, "--days must be positive")
	}
}

func TestLeagueNoRoster(t *testing.T) {
	_, err := League(context.Background(), (*sql.DB)(nil), exampleConfig(t), LeagueOptions{Days: 14}, time.Now())
	want := "no roster (pass --roster or set clustering.entity_catalog.league.roster_path)"
	if err == nil || err.Error() != want {
		t.Fatalf("League() error = %v, want %q", err, want)
	}
}
