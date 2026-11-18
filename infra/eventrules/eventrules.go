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
	"slices"
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

// ReadApplied adapts clusteringrules.ParseGenerated to the cycle's set: the
// types sorted by name, then the rules, actions first, then signals, each
// sorted by target and phrase. A missing file is the empty set.
func ReadApplied(path string) (eventrulecycle.AppliedSet, error) {
	g, err := clusteringrules.ParseGenerated(path)
	if err != nil {
		return eventrulecycle.AppliedSet{}, err
	}
	return appliedSet(g), nil
}

// readAppliedIn is ReadApplied that also rejects a bucket taxonomy does not
// declare and a type it already declares, as clusteringrules.LoadWithGenerated
// does.
func readAppliedIn(path string, taxonomy eventsignals.Taxonomy) (eventrulecycle.AppliedSet, error) {
	g, err := clusteringrules.ParseGenerated(path)
	if err != nil {
		return eventrulecycle.AppliedSet{}, err
	}
	if err := clusteringrules.CheckGeneratedBuckets(path, g, taxonomy); err != nil {
		return eventrulecycle.AppliedSet{}, err
	}
	return appliedSet(g), nil
}

// appliedSet converts g to the cycle's set: types sorted by name (a type is
// referential when its flags are ["referential"]), then the rules, actions
// first, then signals, each sorted by target and phrase.
func appliedSet(g clusteringrules.GeneratedRules) eventrulecycle.AppliedSet {
	out := eventrulecycle.AppliedSet{Rules: []eventrulecycle.AppliedRule{}, Types: []eventrulecycle.AppliedType{}}
	for _, t := range g.Types {
		out.Types = append(out.Types, eventrulecycle.AppliedType{
			Name: t.Name, Description: t.Description, Referential: slices.Equal(t.Flags, []string{string(eventsignals.FlagReferential)}),
			Window: t.Window, ProposalID: t.ProposalID, PromptVersion: t.PromptVersion,
		})
	}
	sort.Slice(out.Types, func(i, j int) bool { return out.Types[i].Name < out.Types[j].Name })
	appendAll := func(kind string, targets []string, entries func(string) []clusteringrules.GeneratedEntry) {
		sort.Strings(targets)
		for _, target := range targets {
			es := append([]clusteringrules.GeneratedEntry(nil), entries(target)...)
			sort.Slice(es, func(i, j int) bool { return es[i].Phrase < es[j].Phrase })
			for _, e := range es {
				out.Rules = append(out.Rules, eventrulecycle.AppliedRule{TargetKind: kind, Target: target, Phrase: e.Phrase, ProposalID: e.ProposalID, PromptVersion: e.PromptVersion})
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
	return out
}

// toGenerated converts the cycle's set to the generated file model; a
// referential type gets the flags ["referential"].
func toGenerated(s eventrulecycle.AppliedSet) (clusteringrules.GeneratedRules, error) {
	g := clusteringrules.GeneratedRules{
		ActionTerms: map[eventsignals.Action][]clusteringrules.GeneratedEntry{},
		ContentCues: map[eventsignals.ContentSignal][]clusteringrules.GeneratedEntry{},
	}
	for _, t := range s.Types {
		gt := clusteringrules.GeneratedType{Name: t.Name, Description: t.Description, Window: t.Window, ProposalID: t.ProposalID, PromptVersion: t.PromptVersion}
		if t.Referential {
			gt.Flags = []string{string(eventsignals.FlagReferential)}
		}
		g.Types = append(g.Types, gt)
	}
	for _, r := range s.Rules {
		e := clusteringrules.GeneratedEntry{Phrase: r.Phrase, ProposalID: r.ProposalID, PromptVersion: r.PromptVersion}
		switch eventruleproposal.TargetKind(r.TargetKind) {
		case eventruleproposal.TargetAction:
			a := eventsignals.Action(r.Target)
			g.ActionTerms[a] = append(g.ActionTerms[a], e)
		case eventruleproposal.TargetSignal:
			sig := eventsignals.ContentSignal(r.Target)
			g.ContentCues[sig] = append(g.ContentCues[sig], e)
		default:
			return g, fmt.Errorf("events: rule %q has unknown target kind %q", r.Phrase, r.TargetKind)
		}
	}
	return g, nil
}

// WriteApplied adapts clusteringrules.WriteGenerated to the cycle's set.
func WriteApplied(path string, s eventrulecycle.AppliedSet) error {
	g, err := toGenerated(s)
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
// with candidate (rules and types) merged as the generated file, and returns the verdict with
// the number of dataset articles whose event type, action or signal the
// candidate changes (0: the gate was not exercised). A missing dataset
// returns ErrGateDatasetMissing.
func RunGate(ctx context.Context, cfg *config.Config, datasetPath string, candidate eventrulecycle.AppliedSet) (eventrulecycle.GateResult, error) {
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
