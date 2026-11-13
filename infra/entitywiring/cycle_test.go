package entitywiring

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestValidateCycleVerification(t *testing.T) {
	for _, ok := range []string{"local", "full", "none"} {
		if err := ValidateCycleVerification(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := ValidateCycleVerification("web"); err == nil {
		t.Error("an unknown mode must be rejected")
	}
}

func TestCycleStages(t *testing.T) {
	var cfg config.Config
	cfg.EntityDiscovery.Wikidata.Enabled = true
	cfg.EntityDiscovery.Verification.WikidataFirst = true
	cfg.EntityDiscovery.Verification.Fallback = true
	cfg.CatalogMaintenance.WebLookup.Enabled = true

	if w, f, c, v := cycleStages(&cfg, CycleVerificationLocal); !w || f || !c || !v {
		t.Errorf("local = wikidata %v fallback %v chooser %v verify %v; want Wikidata and chooser, never the web fallback", w, f, c, v)
	}
	if w, f, c, v := cycleStages(&cfg, CycleVerificationFull); !w || !f || !c || !v {
		t.Errorf("full = %v %v %v %v; want every stage", w, f, c, v)
	}
	if w, f, c, v := cycleStages(&cfg, CycleVerificationNone); w || f || c || v {
		t.Errorf("none = %v %v %v %v; want no verification", w, f, c, v)
	}
	cfg.EntityDiscovery.Wikidata.Enabled = false
	if w, _, _, _ := cycleStages(&cfg, CycleVerificationLocal); w {
		t.Error("local must not enable Wikidata when it is disabled in the configuration")
	}
}

func TestSessionArticleIDs_OnlyPublishedAndRejectedArticlesOnce(t *testing.T) {
	ds := []out.ReviewDecision{
		{ArticleID: "a", Label: "publish"},
		{ArticleID: "b", Label: "reject"},
		{ArticleID: "c", Label: "ambiguous"},
		{ArticleID: "d", Label: ""},
		{ArticleID: "a", Label: "publish"}, // re-decided: still one article
		{ArticleID: "e", Label: "update"},
	}
	got := sessionArticleIDs(ds)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("sessionArticleIDs() = %v, want [a b]: skip, ambiguous, duplicate and update decisions are not part of the sample", got)
	}
}

type stubLLM struct{ out.EntityDiscoveryLLM }

func TestCycleDeps_NoReviewedArticlesBuildsNothing(t *testing.T) {
	db := testDB(t)
	called := false
	newLLM := func(config.EntityDiscoveryConfig, int, bool, out.Logger) out.EntityDiscoveryLLM {
		called = true
		return stubLLM{}
	}
	deps, in, err := CycleDeps(context.Background(), discoveryCfg("http://unused.invalid"), db, "s-empty", CycleVerificationNone, io.Discard, newLLM)
	if err != nil {
		t.Fatalf("CycleDeps() error = %v", err)
	}
	if len(in.SessionArticleIDs) != 0 || deps.LLM != nil || called {
		t.Fatalf("CycleDeps() = %+v, %+v (LLM built %v), want nothing for a session without reviewed articles", deps, in, called)
	}
	if _, _, err := CycleDeps(context.Background(), discoveryCfg("http://unused.invalid"), db, "s-empty", "web", io.Discard, newLLM); err == nil {
		t.Fatal("an unknown verification mode must be rejected")
	}
}

func TestCycleDeps_WiresTheSessionSample(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := sqliteadapter.NewEditorialCycleSessionRepository(db).Create(ctx, out.EditorialCycleSession{
		ID: "s1", StartedAt: time.Now(), UpdatedAt: time.Now(), Status: "completed", CurrentStep: "review", FlagsJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	sourceID, err := source.NewID(source.TypeWeb, "example")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2"} {
		if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source: source.Source{ID: sourceID, Name: "Example", Type: source.TypeWeb},
			Title:  id, Summary: id, FetchedAt: time.Now(), Status: article.StatusFetched,
		}); err != nil {
			t.Fatal(err)
		}
	}
	decisions := sqliteadapter.NewReviewDecisionRepository(db)
	for _, d := range []out.ReviewDecision{
		{SessionID: "s1", ArticleID: "a1", Action: "publish", Label: "publish", DecidedAt: time.Now()},
		{SessionID: "s1", ArticleID: "a2", Action: "skip", Label: "", DecidedAt: time.Now()},
	} {
		if err := decisions.Save(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	cfg := discoveryCfg("http://unused.invalid")
	cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath = emptyCatalogs(t)
	llm := stubLLM{}
	deps, in, err := CycleDeps(ctx, cfg, db, "s1", CycleVerificationNone, io.Discard,
		func(config.EntityDiscoveryConfig, int, bool, out.Logger) out.EntityDiscoveryLLM { return llm })
	if err != nil {
		t.Fatalf("CycleDeps() error = %v", err)
	}
	if len(in.SessionArticleIDs) != 1 || in.SessionArticleIDs[0] != "a1" || in.Limit != 1 || in.BatchSize != 1 ||
		in.ExtractorVersion != "v1" || in.VerifyMinConfidence != cycleVerifyMinConfidence {
		t.Fatalf("input = %+v", in)
	}
	if deps.LLM != llm || deps.Verifier != nil || deps.Apply == nil || deps.Regenerate == nil || deps.Proposals == nil {
		t.Fatalf("deps = %+v, want the injected LLM, no verifier (mode none), and approval/regeneration wired", deps)
	}
}

// WriterLogger keeps the entity commands' "[component] ..." line format,
// which the CLI prints and the panel's step log shows.
func TestWriterLoggerFormat(t *testing.T) {
	var b strings.Builder
	var l out.Logger = WriterLogger{Out: &b, Component: "llm"}
	l.Debug("hidden")
	l.Info("started", "n", 2)
	l.Warn("slow")
	l.Error("failed", errors.New("boom"), "id", "a1")
	want := "[llm] started [n 2]\n[llm] WARN slow []\n[llm] ERROR failed: boom [id a1]\n"
	if b.String() != want {
		t.Errorf("log = %q, want %q", b.String(), want)
	}
}
