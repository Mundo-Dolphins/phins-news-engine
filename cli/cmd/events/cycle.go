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
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultGeneratedRulesPath is where the step writes approved phrases
	// while clustering.generated_rules_path is empty. Writing the file never
	// activates it: activation is the operator's config change.
	DefaultGeneratedRulesPath = "configs/event_rules_generated.yml"
	// DefaultDatasetPath is the gold clustering dataset the gate scores.
	DefaultDatasetPath = "testdata/clustering_groups.yml"

	defaultSampleSize = 60
	defaultSinceDays  = 30
	// controlSampleSize caps the typed control sample: the newest typed
	// articles of the window, re-extracted so the checker can reject a
	// phrase that would change an existing type.
	controlSampleSize = 200
)

// ErrGateDatasetMissing reports that the clustering gate has no dataset to
// score; the step warns and applies nothing.
var ErrGateDatasetMissing = errors.New("clustering gate dataset not found")

// CycleOptions configures RunCycleStep. Zero values take the defaults.
type CycleOptions struct {
	DatasetPath string // gold clustering dataset; DefaultDatasetPath
	SampleSize  int    // generic articles sampled; 60
	SinceDays   int    // sample window in days; 30
}

func (o CycleOptions) withDefaults() CycleOptions {
	if o.DatasetPath == "" {
		o.DatasetPath = DefaultDatasetPath
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

// GeneratedPath is the generated rules file the step reads and writes: the
// configured path, or DefaultGeneratedRulesPath while the feature is off.
func GeneratedPath(cfg *config.Config) string {
	if cfg.Clustering.GeneratedRulesPath != "" {
		return cfg.Clustering.GeneratedRulesPath
	}
	return DefaultGeneratedRulesPath
}

// DeployPaths lists the files the step changed that a deploy must carry:
// the generated rules file, only when rules were applied and it exists.
func DeployPaths(cfg *config.Config, res eventrulecycle.Result) []string {
	if res.Applied == 0 {
		return nil
	}
	path := GeneratedPath(cfg)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return []string{path}
}

// ReadApplied adapts clusteringrules.ParseGenerated to the cycle's rules,
// actions first, then signals, each sorted by target and phrase. A missing
// file is the empty list.
func ReadApplied(path string) ([]eventrulecycle.AppliedRule, error) {
	g, err := clusteringrules.ParseGenerated(path)
	if err != nil {
		return nil, err
	}
	out := []eventrulecycle.AppliedRule{}
	appendAll := func(kind string, targets []string, entries func(string) []clusteringrules.GeneratedEntry) {
		sort.Strings(targets)
		for _, target := range targets {
			es := append([]clusteringrules.GeneratedEntry(nil), entries(target)...)
			sort.Slice(es, func(i, j int) bool { return es[i].Phrase < es[j].Phrase })
			for _, e := range es {
				out = append(out, eventrulecycle.AppliedRule{TargetKind: kind, Target: target, Phrase: e.Phrase, ProposalID: e.ProposalID, PromptVersion: e.PromptVersion})
			}
		}
	}
	actions := make([]string, 0, len(g.ActionTerms))
	for a := range g.ActionTerms {
		actions = append(actions, string(a))
	}
	appendAll(string(eventruleproposal.TargetAction), actions, func(t string) []clusteringrules.GeneratedEntry { return g.ActionTerms[eventsignals.Action(t)] })
	signals := make([]string, 0, len(g.ContentCues))
	for s := range g.ContentCues {
		signals = append(signals, string(s))
	}
	appendAll(string(eventruleproposal.TargetSignal), signals, func(t string) []clusteringrules.GeneratedEntry { return g.ContentCues[eventsignals.ContentSignal(t)] })
	return out, nil
}

// toGenerated converts the cycle's rules to the generated file model.
func toGenerated(rules []eventrulecycle.AppliedRule) (clusteringrules.GeneratedRules, error) {
	g := clusteringrules.GeneratedRules{
		ActionTerms: map[eventsignals.Action][]clusteringrules.GeneratedEntry{},
		ContentCues: map[eventsignals.ContentSignal][]clusteringrules.GeneratedEntry{},
	}
	for _, r := range rules {
		e := clusteringrules.GeneratedEntry{Phrase: r.Phrase, ProposalID: r.ProposalID, PromptVersion: r.PromptVersion}
		switch eventruleproposal.TargetKind(r.TargetKind) {
		case eventruleproposal.TargetAction:
			a := eventsignals.Action(r.Target)
			g.ActionTerms[a] = append(g.ActionTerms[a], e)
		case eventruleproposal.TargetSignal:
			s := eventsignals.ContentSignal(r.Target)
			g.ContentCues[s] = append(g.ContentCues[s], e)
		default:
			return g, fmt.Errorf("events: rule %q has unknown target kind %q", r.Phrase, r.TargetKind)
		}
	}
	return g, nil
}

// WriteApplied adapts clusteringrules.WriteGenerated to the cycle's rules.
func WriteApplied(path string, rules []eventrulecycle.AppliedRule) error {
	g, err := toGenerated(rules)
	if err != nil {
		return err
	}
	return clusteringrules.WriteGenerated(path, g)
}

// handWrittenRules loads the hand-written rules plus the entity catalog,
// without any generated file.
func handWrittenRules(cfg *config.Config) (clusteringrules.RulesFile, error) {
	return clusteringrules.LoadWithEntityCatalog(cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
}

// RunGate scores the gold dataset with the hand-written rules (baseline) and
// with candidate merged as the generated file, and returns the verdict with
// the number of dataset articles whose event type, action or signal the
// candidate changes (0: the gate was not exercised). A missing dataset
// returns ErrGateDatasetMissing.
func RunGate(ctx context.Context, cfg *config.Config, datasetPath string, candidate []eventrulecycle.AppliedRule) (eventrulecycle.GateResult, error) {
	var zero eventrulecycle.GateResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	dataset, err := clustereval.LoadDataset(datasetPath)
	if errors.Is(err, fs.ErrNotExist) {
		return zero, fmt.Errorf("%w: %s", ErrGateDatasetMissing, datasetPath)
	}
	if err != nil {
		return zero, err
	}
	baseline, err := handWrittenRules(cfg)
	if err != nil {
		return zero, err
	}
	dir, err := os.MkdirTemp("", "event-rules-gate-")
	if err != nil {
		return zero, fmt.Errorf("events: gate temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmp := filepath.Join(dir, "event_rules_generated.yml")
	if err := WriteApplied(tmp, candidate); err != nil {
		return zero, err
	}
	withCandidate, _, err := clusteringrules.LoadWithGenerated(baseline, tmp)
	if err != nil {
		return zero, err
	}
	now := clustereval.LatestPublishedAt(dataset)
	score := func(f clusteringrules.RulesFile) (clustereval.Report, error) {
		return clustereval.Evaluate(dataset, f.ToEventSignalsRules(), f.ToClusteringConfig(), f.ToRepresentativeConfig(), now)
	}
	before, err := score(baseline)
	if err != nil {
		return zero, err
	}
	after, err := score(withCandidate)
	if err != nil {
		return zero, err
	}
	affected, err := clustereval.CountChangedSignals(dataset, baseline.ToEventSignalsRules(), withCandidate.ToEventSignalsRules(), now)
	if err != nil {
		return zero, err
	}
	res := eventrulecycle.EvaluateGate(before, after)
	res.Affected = affected
	return res, nil
}

// RunCycleStep runs the event rules step: discovery over a sample of generic
// articles, interactive review on scanner, and the gated apply to
// GeneratedPath(cfg). It is the same code path as `events review`.
func RunCycleStep(cmd *cobra.Command, cfg *config.Config, db *sql.DB, scanner *bufio.Scanner, opts CycleOptions) (eventrulecycle.Result, error) {
	opts = opts.withDefaults()
	generatedPath := GeneratedPath(cfg)
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
			f, err := handWrittenRules(cfg)
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
			return RunGate(ctx, cfg, opts.DatasetPath, candidate)
		},
		ReadApplied:   ReadApplied,
		WriteApplied:  WriteApplied,
		GeneratedPath: generatedPath,
		Now:           time.Now,
	}
	res, err := eventrulecycle.Run(cmd.Context(), deps, eventrulecycle.Input{
		BatchSize:    cfg.OfflineTraining.LLM.BatchSize,
		CheckOptions: eventruleproposal.DefaultCheckOptions(),
		Scanner:      scanner,
		Out:          cmd.OutOrStdout(),
	})
	if errors.Is(err, ErrGateDatasetMissing) {
		res.GateBlocked = true
		res.GateReason = err.Error()
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: event rules: %v; approved rules stay approved and nothing was applied.\n", err)
		return res, nil
	}
	return res, err
}
