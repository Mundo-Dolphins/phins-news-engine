package pipelinereplay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/replayfixtures"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestWire_AcceptsFreshRelevantArticle(t *testing.T) {
	cfgPath := replayfixtures.WriteTestConfig(t, replayfixtures.TestDictionaryYAML(t), "disabled", "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	labPath := filepath.Join(t.TempDir(), "lab.db")
	labDB, err := infrasqlite.Open(labPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open lab db: %v", err)
	}
	defer labDB.Close()
	if err := infrasqlite.Migrate(context.Background(), labDB, migrations.FS); err != nil {
		t.Fatalf("migrate lab db: %v", err)
	}

	pc, articles, fetcher, clock, err := Wire(cfg, labDB, Overrides{})
	if err != nil {
		t.Fatalf("Wire() error = %v", err)
	}

	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	clock.Set(now)
	fetcher.Set([]article.Article{{
		ID:           "test-1",
		Title:        "Springfield Isotopes sign a guy",
		OriginalURL:  "https://example.com/story",
		CanonicalURL: "https://example.com/story",
		Source:       source.Source{ID: replayfixtures.MustSourceID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS, Domain: "example.com"},
		PublishedAt:  now.Add(-1 * time.Hour),
		FetchedAt:    now,
	}})

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got, err := articles.FindByID(context.Background(), "test-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusPendingPublication {
		t.Errorf("Status = %v, want %v", got.Status, article.StatusPendingPublication)
	}
}

// TestWire_RegistersEditorialModelForFK exercises the model_registry half
// of Wire's minimal-registration fix: with editorial.mode set to "shadow"
// and model.path pointing at a real editorialmodel.Save-written fixture,
// ProcessCycle's Step 7 sets a.ClassifierVersion to the loaded model's own
// version (see process_cycle.go), and articles.classifier_version FKs into
// model_registry(version) (migration 0017). If Wire failed to register
// that version, saving the article would fail its foreign key check
// exactly like the dictionary_registry bug this test's sibling guards
// against.
func TestWire_RegistersEditorialModelForFK(t *testing.T) {
	modelPath := replayfixtures.TestEditorialModelDir(t)
	cfgPath := replayfixtures.WriteTestConfig(t, replayfixtures.TestDictionaryYAML(t), "shadow", modelPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	labPath := filepath.Join(t.TempDir(), "lab.db")
	labDB, err := infrasqlite.Open(labPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open lab db: %v", err)
	}
	defer labDB.Close()
	if err := infrasqlite.Migrate(context.Background(), labDB, migrations.FS); err != nil {
		t.Fatalf("migrate lab db: %v", err)
	}

	pc, articles, fetcher, clock, err := Wire(cfg, labDB, Overrides{})
	if err != nil {
		t.Fatalf("Wire() error = %v", err)
	}

	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	clock.Set(now)
	fetcher.Set([]article.Article{{
		ID:           "test-1",
		Title:        "Springfield Isotopes sign a guy",
		OriginalURL:  "https://example.com/story",
		CanonicalURL: "https://example.com/story",
		Source:       source.Source{ID: replayfixtures.MustSourceID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS, Domain: "example.com"},
		PublishedAt:  now.Add(-1 * time.Hour),
		FetchedAt:    now,
	}})

	// A FK failure here (constraint failed: FOREIGN KEY constraint failed)
	// would mean Wire never wrote the model_registry row before Step 7 set
	// a.ClassifierVersion to the real model version.
	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got, err := articles.FindByID(context.Background(), "test-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.ClassifierVersion != "editorial-test-v1" {
		t.Errorf("ClassifierVersion = %q, want %q (proves the real loaded model's version was used and saved)", got.ClassifierVersion, "editorial-test-v1")
	}
}

// TestWire_UsesOverrideDictionaryPath proves overrides actually replace
// cfg's own dictionary path rather than being silently ignored: cfg points
// at a dictionary with no entries at all (so classification.Decide would
// never accept anything against it, and editorial.mode stays "disabled" so
// classification alone gates the article), while the override dictionary
// is the real Isotopes fixture — only the override should let the article
// through.
func TestWire_UsesOverrideDictionaryPath(t *testing.T) {
	emptyDictPath := writeEmptyDictionaryYAML(t)
	overrideDictPath := replayfixtures.TestDictionaryYAML(t)
	cfgPath := replayfixtures.WriteTestConfig(t, emptyDictPath, "disabled", "")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	labPath := filepath.Join(t.TempDir(), "lab.db")
	labDB, err := infrasqlite.Open(labPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open lab db: %v", err)
	}
	defer labDB.Close()
	if err := infrasqlite.Migrate(context.Background(), labDB, migrations.FS); err != nil {
		t.Fatalf("migrate lab db: %v", err)
	}

	pc, articles, fetcher, clock, err := Wire(cfg, labDB, Overrides{DictionaryPath: overrideDictPath})
	if err != nil {
		t.Fatalf("Wire() error = %v", err)
	}

	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	clock.Set(now)
	fetcher.Set([]article.Article{{
		ID:           "test-1",
		Title:        "Springfield Isotopes sign a guy",
		OriginalURL:  "https://example.com/story",
		CanonicalURL: "https://example.com/story",
		Source:       source.Source{ID: replayfixtures.MustSourceID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS, Domain: "example.com"},
		PublishedAt:  now.Add(-1 * time.Hour),
		FetchedAt:    now,
	}})

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got, err := articles.FindByID(context.Background(), "test-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusPendingPublication {
		t.Errorf("Status = %v, want %v (proves the override dictionary, not cfg's own, was used)", got.Status, article.StatusPendingPublication)
	}
}

// writeEmptyDictionaryYAML writes a minimal-but-valid dictionary with no
// entries, so classification.Decide never accepts any article against it.
func writeEmptyDictionaryYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-dictionary.yml")
	contents := `
meta:
  version: "1.0.0"
  name: empty_dictionary
  language: en

decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3

categories: {}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write empty dictionary fixture: %v", err)
	}
	return path
}

func repoClusteringConfig(t *testing.T) *config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Clustering.Enabled = true
	cfg.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	cfg.Clustering.EntityCatalog.ManualPath = repofs.ExampleConfig(t, "entity_catalog_manual.yml")
	cfg.Clustering.EntityCatalog.GeneratedPath = repofs.ExampleConfig(t, "entity_catalog_generated.yml")
	return &cfg
}

func TestLoadClusterRules_EmptyGeneratedPathIsTheHandWrittenSet(t *testing.T) {
	cfg := repoClusteringConfig(t)
	got, err := loadClusterRules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := clusteringrules.LoadWithEntityCatalog(cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("with generated_rules_path empty the replay rules must equal the hand-written set")
	}
}

func TestLoadClusterRules_MergesTheGeneratedRulesLikeMain(t *testing.T) {
	cfg := repoClusteringConfig(t)
	cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "event_rules_generated.yml")
	body := "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: v1}\n"
	if err := os.WriteFile(cfg.Clustering.GeneratedRulesPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadClusterRules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.ActionTerms[eventsignals.ActionSign], "zzqx pens a pact") {
		t.Fatalf("sign terms = %v; the generated phrase must be merged", got.ActionTerms[eventsignals.ActionSign])
	}
}
