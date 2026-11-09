// cli/cmd/events/cycle.go
package events

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	defaultSampleSize = 60
	defaultSinceDays  = 30
	// controlSampleSize caps the typed control sample: the newest typed
	// articles of the window, re-extracted so the checker can reject a
	// phrase that would change an existing type.
	controlSampleSize = 200
)

// CycleOptions configures RunCycleStep. Zero values take the defaults.
type CycleOptions struct {
	DatasetPath string // gold clustering dataset; eventrules.DefaultDatasetPath
	SampleSize  int    // generic articles sampled; 60
	SinceDays   int    // sample window in days; 30
}

func (o CycleOptions) withDefaults() CycleOptions {
	if o.DatasetPath == "" {
		o.DatasetPath = eventrules.DefaultDatasetPath
	}
	if o.SampleSize <= 0 {
		o.SampleSize = defaultSampleSize
	}
	if o.SinceDays <= 0 {
		o.SinceDays = defaultSinceDays
	}
	return o
}

// NewCycleLLM builds the discovery LLM from offline_training.llm; a variable
// so tests can substitute it.
var NewCycleLLM = func(_ *cobra.Command, cfg config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM {
	return llmclient.New(shared.OfflineTrainingLLMClientConfig(cfg))
}

// CycleStepDisabledReason returns why the event rules step cannot run with
// cfg, or "" when it can.
func CycleStepDisabledReason(cfg *config.Config) string {
	switch {
	case cfg.OfflineTraining.LLM.BaseURL == "" || cfg.OfflineTraining.LLM.Model == "":
		return "offline_training.llm.base_url and model are not configured"
	case cfg.Clustering.RulesPath == "":
		return "clustering.rules_path is empty"
	case cfg.Clustering.EntityCatalog.ManualPath == "" || cfg.Clustering.EntityCatalog.GeneratedPath == "":
		return "clustering.entity_catalog paths are not configured"
	}
	return ""
}

// RunCycleStep runs the event rules step: discovery over a sample of generic
// articles, interactive review on scanner, and the gated apply to
// eventrules.GeneratedPath(cfg). It is the same code path as `events review`.
func RunCycleStep(cmd *cobra.Command, cfg *config.Config, db *sql.DB, scanner *bufio.Scanner, opts CycleOptions) (eventrulecycle.Result, error) {
	opts = opts.withDefaults()
	generatedPath := eventrules.GeneratedPath(cfg)
	// Pre-flight: warn before the review, so the operator does not approve
	// into an apply the gate will refuse.
	if _, err := os.Stat(opts.DatasetPath); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: event rules: the clustering gate dataset is missing (%s); approvals in this run stay approved and nothing will be applied.\n", opts.DatasetPath)
	}
	lister := sqliteadapter.NewGenericArticleLister(db)
	since := time.Now().UTC().AddDate(0, 0, -opts.SinceDays)
	deps := eventrulecycle.Deps{
		Sampler: func(ctx context.Context) ([]eventruleproposal.Sample, error) {
			return eventrulecycle.Draw(ctx, lister, eventrulecycle.SampleOptions{Since: since, Size: opts.SampleSize})
		},
		Control: func(ctx context.Context) ([]eventruleproposal.Sample, error) {
			return lister.ListTyped(ctx, since, controlSampleSize)
		},
		LLM:       NewCycleLLM(cmd, cfg.OfflineTraining.LLM),
		Proposals: sqliteeventruleproposal.NewRepository(db),
		BaseRules: func() (eventsignals.Rules, error) {
			f, err := eventrules.HandWrittenRules(cfg)
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
				cmd.PrintErrln("warning: " + w)
			}
			return f.ToEventSignalsRules(), nil
		},
		Gate: func(ctx context.Context, candidate []eventrulecycle.AppliedRule) (eventrulecycle.GateResult, error) {
			return eventrules.RunGate(ctx, cfg, opts.DatasetPath, candidate)
		},
		ReadApplied:   eventrules.ReadApplied,
		WriteApplied:  eventrules.WriteApplied,
		GeneratedPath: generatedPath,
		Now:           time.Now,
	}
	res, err := eventrulecycle.Run(cmd.Context(), deps, eventrulecycle.Input{
		BatchSize:    cfg.OfflineTraining.LLM.BatchSize,
		CheckOptions: eventruleproposal.DefaultCheckOptions(),
		Scanner:      scanner,
		Out:          cmd.OutOrStdout(),
	})
	if errors.Is(err, eventrules.ErrGateDatasetMissing) {
		res.GateBlocked = true
		res.GateReason = err.Error()
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: event rules: %v; approved rules stay approved and nothing was applied.\n", err)
		return res, nil
	}
	return res, err
}
