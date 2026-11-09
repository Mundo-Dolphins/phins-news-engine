package entitywiring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func nopLoggers(string) out.Logger { return nopLogger{} }

// emptyCatalogs writes empty manual and generated catalogs.
func emptyCatalogs(t *testing.T) (manual, generated string) {
	t.Helper()
	dir := t.TempDir()
	manual, generated = filepath.Join(dir, "manual.yml"), filepath.Join(dir, "generated.yml")
	for _, p := range []string{manual, generated} {
		if err := os.WriteFile(p, []byte("entities: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return manual, generated
}

func discoveryCfg(baseURL string) *config.Config {
	cfg := &config.Config{}
	cfg.EntityDiscovery = config.EntityDiscoveryConfig{
		Enabled: true, ExtractorVersion: "v1", BatchSize: 1,
		LLM: config.EntityDiscoveryLLMConfig{BaseURL: baseURL, Model: "test-model", TimeoutSeconds: 5},
	}
	return cfg
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDiscoveryConfigDisabledNeedsBaseURL(t *testing.T) {
	cfg := discoveryCfg("http://unused.invalid")
	cfg.EntityDiscovery.Enabled = false
	if _, _, _, err := DiscoveryConfig(cfg, LLMOverrides{}); err == nil || !strings.Contains(err.Error(), "entity_discovery.enabled is false") {
		t.Fatalf("DiscoveryConfig() error = %v, want the disabled error", err)
	}
	maxTokens, off := 64, true
	dc, mt, dt, err := DiscoveryConfig(cfg, LLMOverrides{BaseURL: "http://other.invalid", MaxTokens: &maxTokens, DisableThinking: &off})
	if err != nil || !dc.Enabled || dc.LLM.BaseURL != "http://other.invalid" || mt != 64 || !dt {
		t.Fatalf("DiscoveryConfig(override) = %+v, %d, %v, %v", dc.LLM, mt, dt, err)
	}
}

func TestResolveStagesOfflineWins(t *testing.T) {
	on := config.VerificationConfig{WikidataFirst: true, Fallback: true}
	if wd, fb := ResolveStages(on, true, Stages{Offline: true, Wikidata: true, Fallback: true}); wd || fb {
		t.Fatalf("ResolveStages(offline) = %v, %v; want false, false", wd, fb)
	}
}

func TestChainVerifierNilWhenNoStage(t *testing.T) {
	v, extras, err := ChainVerifier(context.Background(), nil, &config.Config{}, nil, entitydiscovery.CatalogIndex{}, false, false, false, nopLoggers)
	if v != nil || extras != nil || err != nil {
		t.Fatalf("ChainVerifier() = %v, %v, %v; want nil, nil, nil", v, extras != nil, err)
	}
}

func TestDiscoverFailedBatchesNotAnError(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	sourceID, err := source.NewID(source.TypeWeb, "example-com")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, a := range []struct{ id, title string }{
		{"a1", "Isotopes sign CB Jai'Rene' Mossbank"},
		{"a2", "Sharks claim LB Quentin Halloway"},
	} {
		if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
			ID: a.id, OriginalURL: "https://example.com/" + a.id, CanonicalURL: "https://example.com/" + a.id,
			Source: source.Source{ID: sourceID, Name: "Example", Type: source.TypeWeb},
			Title:  a.title, Summary: a.title, FetchedAt: now, Status: article.StatusFetched,
		}); err != nil {
			t.Fatalf("save article: %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Mossbank") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"entities":[{"name":"Quentin Halloway","type":"player","aliases":["Halloway"],"metadata":{"position":"LB"},"confidence":0.9}]}`,
		}}}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	manual, generated := emptyCatalogs(t)
	rep, err := Discover(ctx, db, discoveryCfg(srv.URL), DiscoverFlags{
		ManualPath: manual, GeneratedPath: generated, VerifyMinConfidence: 0.85,
	}, time.Now(), nopLoggers)
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil with a failed batch", err)
	}
	if rep.Result.BatchesTotal != 2 || rep.Result.BatchesFailed != 1 {
		t.Fatalf("batches = %d (failed %d), want 2 (failed 1)", rep.Result.BatchesTotal, rep.Result.BatchesFailed)
	}
	saved, err := sqliteentityproposal.NewEntityProposalRepository(db).List(ctx, "", "pending")
	if err != nil || len(saved) != 1 || saved[0].CanonicalName != "Quentin Halloway" {
		t.Fatalf("pending proposals = %+v, %v; want the successful batch's proposal", saved, err)
	}
}

func TestVerifyProposalsBadType(t *testing.T) {
	// A nil DB and config prove validation happens before either is used.
	_, err := VerifyProposals(context.Background(), nil, nil, VerifyFlags{EntityType: "mascot"}, nopLoggers)
	if err == nil || !strings.Contains(err.Error(), "--type") {
		t.Fatalf("VerifyProposals() error = %v, want --type validation error", err)
	}
	_, err = VerifyProposals(context.Background(), nil, nil, VerifyFlags{Provider: "llm"}, nopLoggers)
	if err == nil || !strings.Contains(err.Error(), "--provider") {
		t.Fatalf("VerifyProposals() error = %v, want --provider validation error", err)
	}
}

func TestVerifyProposalsNoStage(t *testing.T) {
	manual, generated := emptyCatalogs(t)
	_, err := VerifyProposals(context.Background(), nil, discoveryCfg("http://unused.invalid"), VerifyFlags{
		ManualPath: manual, GeneratedPath: generated, Stages: Stages{Offline: true},
	}, nopLoggers)
	if !errors.Is(err, ErrNoStage) {
		t.Fatalf("VerifyProposals() error = %v, want ErrNoStage", err)
	}
}

func TestEvaluateLeagueDisabled(t *testing.T) {
	manual, generated := emptyCatalogs(t)
	_, err := EvaluateLeague(context.Background(), nil, &config.Config{}, manual, generated, time.Now(), nopLoggers)
	if !errors.Is(err, ErrLeagueDisabled) {
		t.Fatalf("EvaluateLeague() error = %v, want ErrLeagueDisabled", err)
	}
}

func TestVerifyNoEffectWarning(t *testing.T) {
	on, off := true, false
	cfg := &config.Config{} // verification.fallback false
	if w := VerifyNoEffectWarning(cfg, DiscoverFlags{Verify: &on}); w != verifyNoEffectWarning {
		t.Fatalf("forced verify without fallback: %q", w)
	}
	for name, f := range map[string]DiscoverFlags{
		"verify unset": {},
		"verify false": {Verify: &off},
		"--fallback":   {Verify: &on, Stages: Stages{Fallback: true}},
		"--offline":    {Verify: &on, Stages: Stages{Offline: true}},
	} {
		if w := VerifyNoEffectWarning(cfg, f); w != "" {
			t.Errorf("%s: warning = %q, want none", name, w)
		}
	}
	cfg.EntityDiscovery.Verification.Fallback = true
	if w := VerifyNoEffectWarning(cfg, DiscoverFlags{Verify: &on}); w != "" {
		t.Errorf("config fallback on: warning = %q, want none", w)
	}
}
