package entities

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
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

func TestCycleStepDisabledReason(t *testing.T) {
	var cfg config.Config
	if CycleStepDisabledReason(&cfg) == "" {
		t.Fatal("a configuration without entity_discovery must report why the step is off")
	}
	cfg.EntityDiscovery.Enabled = true
	cfg.Clustering.EntityCatalog.ManualPath = "m.yml"
	cfg.Clustering.EntityCatalog.GeneratedPath = "g.yml"
	cfg.Dictionary.Path = "d.yml"
	if r := CycleStepDisabledReason(&cfg); r != "" {
		t.Fatalf("a fully configured step must run, got %q", r)
	}
	cfg.Dictionary.Path = ""
	if CycleStepDisabledReason(&cfg) == "" {
		t.Fatal("a missing dictionary path must disable the step")
	}
}

func TestSessionArticleIDs_OnlyPublishedAndRejectedArticlesOnce(t *testing.T) {
	ds := []appout.ReviewDecision{
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
