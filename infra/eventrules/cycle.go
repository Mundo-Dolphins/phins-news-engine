// infra/eventrules/cycle.go
package eventrules

import (
	"context"
	"database/sql"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultSampleSize is how many generic articles discovery samples.
	DefaultSampleSize = 60
	// DefaultSinceDays is the sample window in days before now.
	DefaultSinceDays = 30
	// ControlSampleSize caps the typed control sample: the newest typed
	// articles of the window, re-extracted so the checker can reject a
	// phrase that would change an existing type.
	ControlSampleSize = 200
)

// CycleStepDisabledReason returns why the event rules step cannot run with
// cfg, or "" when it can.
func CycleStepDisabledReason(cfg *config.Config) string {
	if cfg.OfflineTraining.LLM.BaseURL == "" || cfg.OfflineTraining.LLM.Model == "" {
		return "offline_training.llm.base_url and model are not configured"
	}
	return ReviewDisabledReason(cfg)
}

// ReviewDisabledReason returns why the event rules can be neither reviewed
// nor applied with cfg (the LLM aside, which only discovery needs), or ""
// when they can. Its texts are those of CycleStepDisabledReason.
func ReviewDisabledReason(cfg *config.Config) string {
	switch {
	case cfg.Clustering.RulesPath == "":
		return "clustering.rules_path is empty"
	case cfg.Clustering.EntityCatalog.ManualPath == "" || cfg.Clustering.EntityCatalog.GeneratedPath == "":
		return "clustering.entity_catalog paths are not configured"
	}
	return ""
}

// CycleDeps wires the event rule cycle to cfg and db with the default
// sample (DefaultSampleSize generic articles of the last DefaultSinceDays
// days, ControlSampleSize typed ones), the gate scored on datasetPath and
// GeneratedPath(cfg) as the generated rules file. warn receives each
// warning of loading the generated file (nil drops them); now is the clock
// (nil is time.Now) and fixes the sample window when CycleDeps is called.
func CycleDeps(cfg *config.Config, db *sql.DB, datasetPath string, llm appout.EventRuleDiscoveryLLM, warn func(string), now func() time.Time) eventrulecycle.Deps {
	return CycleDepsSized(cfg, db, datasetPath, DefaultSampleSize, DefaultSinceDays, llm, warn, now)
}

// CycleDepsSized is CycleDeps with sampleSize generic articles of the last
// sinceDays days, for the CLI's --sample-size and --since-days flags.
func CycleDepsSized(cfg *config.Config, db *sql.DB, datasetPath string, sampleSize, sinceDays int, llm appout.EventRuleDiscoveryLLM, warn func(string), now func() time.Time) eventrulecycle.Deps {
	if now == nil {
		now = time.Now
	}
	if warn == nil {
		warn = func(string) {}
	}
	generatedPath := GeneratedPath(cfg)
	lister := sqliteadapter.NewGenericArticleLister(db)
	since := now().UTC().AddDate(0, 0, -sinceDays)
	return eventrulecycle.Deps{
		Sampler: func(ctx context.Context) ([]eventruleproposal.Sample, error) {
			return eventrulecycle.Draw(ctx, lister, eventrulecycle.SampleOptions{Since: since, Size: sampleSize})
		},
		Control: func(ctx context.Context) ([]eventruleproposal.Sample, error) {
			return lister.ListTyped(ctx, since, ControlSampleSize)
		},
		LLM:       llm,
		Proposals: sqliteeventruleproposal.NewRepository(db),
		BaseRules: func() (eventsignals.Rules, error) {
			f, err := HandWrittenRules(cfg)
			if err != nil {
				return eventsignals.Rules{}, err
			}
			// Merged so Impact sees the phrases already applied, whether or
			// not the operator has activated the file yet.
			f, warnings, err := clusteringrules.LoadWithGenerated(f, generatedPath)
			if err != nil {
				return eventsignals.Rules{}, err
			}
			for _, w := range warnings {
				warn(w)
			}
			return f.ToEventSignalsRules(), nil
		},
		Gate: func(ctx context.Context, candidate []eventrulecycle.AppliedRule) (eventrulecycle.GateResult, error) {
			return RunGate(ctx, cfg, datasetPath, candidate)
		},
		ReadApplied:   ReadApplied,
		WriteApplied:  WriteApplied,
		GeneratedPath: generatedPath,
		Now:           now,
	}
}
