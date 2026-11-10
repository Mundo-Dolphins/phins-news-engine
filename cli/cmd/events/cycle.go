// cli/cmd/events/cycle.go
package events

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
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
		o.SampleSize = eventrules.DefaultSampleSize
	}
	if o.SinceDays <= 0 {
		o.SinceDays = eventrules.DefaultSinceDays
	}
	return o
}

// NewCycleLLM builds the discovery LLM from offline_training.llm; a variable
// so tests can substitute it.
var NewCycleLLM = func(_ *cobra.Command, cfg config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM {
	return llmclient.New(shared.OfflineTrainingLLMClientConfig(cfg))
}

// CycleStepDisabledReason returns why the event rules step cannot run with
// cfg, or "" when it can; see eventrules.CycleStepDisabledReason.
func CycleStepDisabledReason(cfg *config.Config) string {
	return eventrules.CycleStepDisabledReason(cfg)
}

// RunCycleStep runs the event rules step: discovery over a sample of generic
// articles, interactive review on scanner, and the gated apply to
// eventrules.GeneratedPath(cfg). It is the same code path as `events review`.
func RunCycleStep(cmd *cobra.Command, cfg *config.Config, db *sql.DB, scanner *bufio.Scanner, opts CycleOptions) (eventrulecycle.Result, error) {
	opts = opts.withDefaults()
	// Pre-flight: warn before the review, so the operator does not approve
	// into an apply the gate will refuse.
	if _, err := os.Stat(opts.DatasetPath); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: event rules: the clustering gate dataset is missing (%s); approvals in this run stay approved and nothing will be applied.\n", opts.DatasetPath)
	}
	deps := eventrules.CycleDepsSized(cfg, db, opts.DatasetPath, opts.SampleSize, opts.SinceDays,
		NewCycleLLM(cmd, cfg.OfflineTraining.LLM),
		func(w string) { cmd.PrintErrln("warning: " + w) },
		time.Now)
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
