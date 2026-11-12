// infra/eventrules/cycle_test.go
package eventrules

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type nopLLM struct{}

func (nopLLM) DiscoverTopics(context.Context, []appout.EventRuleArticle) ([]appout.EventTopicProposal, error) {
	return nil, nil
}

func (nopLLM) DiscoverPhrases(context.Context, []appout.EventRuleArticle, []string, []string) ([]appout.EventPhraseProposal, error) {
	return nil, nil
}

func TestCycleDepsDefaults(t *testing.T) {
	if DefaultSampleSize != 60 || DefaultSinceDays != 30 || ControlSampleSize != 200 {
		t.Fatalf("defaults %d/%d/%d", DefaultSampleSize, DefaultSinceDays, ControlSampleSize)
	}
	cfg := enabledConfig(t)
	cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "generated.yml")
	db := openTestDB(t)
	seedArticles(t, db, "g", eventsignals.EventTypeGeneric, "Isotopes inks a deal with veteran", "Weather in Springfield")
	seedArticles(t, db, "t", "transaction", "Isotopes sign veteran linebacker")
	llm := nopLLM{}
	fixed := time.Now().UTC()
	var warnings []string
	d := CycleDeps(cfg, db, filepath.Join(t.TempDir(), "missing.yml"), llm, func(w string) { warnings = append(warnings, w) }, func() time.Time { return fixed })

	if d.GeneratedPath != cfg.Clustering.GeneratedRulesPath || d.LLM != llm || d.Proposals == nil ||
		d.ReadApplied == nil || d.WriteApplied == nil || !d.Now().Equal(fixed) {
		t.Fatalf("deps %+v", d)
	}
	ctx := context.Background()
	sample, err := d.Sampler(ctx)
	if err != nil || len(sample) != 2 {
		t.Fatalf("sample %+v, %v", sample, err)
	}
	control, err := d.Control(ctx)
	if err != nil || len(control) != 1 || control[0].Article.ID != "t0" {
		t.Fatalf("control %+v, %v", control, err)
	}
	if _, err := d.BaseRules(); err != nil {
		t.Fatalf("base rules: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings %v", warnings)
	}
	if _, err := d.Gate(ctx, nil); !errors.Is(err, ErrGateDatasetMissing) || !strings.Contains(err.Error(), "missing.yml") {
		t.Errorf("gate err %v, want the dataset path passed in", err)
	}

	// The window is DefaultSinceDays before now: 40 days on, nothing is in it.
	later := CycleDeps(cfg, db, DefaultDatasetPath, llm, nil, func() time.Time { return fixed.AddDate(0, 0, 40) })
	if sample, err := later.Sampler(ctx); err != nil || len(sample) != 0 {
		t.Errorf("sample 40 days on %+v, %v", sample, err)
	}
}

func TestCycleStepDisabledReason(t *testing.T) {
	cfg := enabledConfig(t)
	cfg.OfflineTraining.LLM.BaseURL = "http://127.0.0.1:1/v1"
	cfg.OfflineTraining.LLM.Model = "test"
	if r := CycleStepDisabledReason(cfg); r != "" {
		t.Fatalf("a fully configured step must run, got %q", r)
	}
	for want, mutate := range map[string]func(*config.Config){
		"offline_training.llm.base_url and model are not configured": func(c *config.Config) { c.OfflineTraining.LLM.Model = "" },
		"clustering.rules_path is empty":                             func(c *config.Config) { c.Clustering.RulesPath = "" },
		"clustering.entity_catalog paths are not configured":         func(c *config.Config) { c.Clustering.EntityCatalog.GeneratedPath = "" },
	} {
		c := *cfg
		mutate(&c)
		if got := CycleStepDisabledReason(&c); got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
	}
}
