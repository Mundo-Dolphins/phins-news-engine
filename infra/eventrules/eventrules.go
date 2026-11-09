// infra/eventrules/eventrules.go

// Package eventrules holds the event rule operations behind the `events`
// CLI commands and the admin actions: generated rules file handling, the
// clustering gate, the impact report and the decisions on new event type
// proposals. It does not import the cli tree.
package eventrules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

const (
	// DefaultGeneratedRulesPath is where the step writes approved phrases
	// while clustering.generated_rules_path is empty. Writing the file never
	// activates it: activation is the operator's config change.
	DefaultGeneratedRulesPath = "configs/event_rules_generated.yml"
	// DefaultDatasetPath is the gold clustering dataset the gate scores.
	DefaultDatasetPath = "testdata/clustering_groups.yml"
)

// ErrGateDatasetMissing reports that the clustering gate has no dataset to
// score; the step warns and applies nothing.
var ErrGateDatasetMissing = errors.New("clustering gate dataset not found")

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

// HandWrittenRules loads the hand-written rules plus the entity catalog,
// without any generated file.
func HandWrittenRules(cfg *config.Config) (clusteringrules.RulesFile, error) {
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
	baseline, err := HandWrittenRules(cfg)
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
