// infra/clusteringrules/generated.go
package clusteringrules

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// GeneratedEntry is one operator-approved trigger phrase with its provenance.
type GeneratedEntry struct {
	Phrase        string `yaml:"phrase"`
	ProposalID    string `yaml:"proposal_id"`
	PromptVersion string `yaml:"prompt_version"`
}

// GeneratedType is one operator-approved event type with its provenance.
// It declares an event type and one action of the same name; its trigger
// phrases live in the action_terms bucket of that name. Window is a
// duration string ("48h"); Flags may only hold "referential".
type GeneratedType struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Flags         []string `yaml:"flags,omitempty"`
	Window        string   `yaml:"window"`
	ProposalID    string   `yaml:"proposal_id"`
	PromptVersion string   `yaml:"prompt_version"`
}

// GeneratedRules is the content of configs/event_rules_generated.yml.
type GeneratedRules struct {
	Types       []GeneratedType
	ActionTerms map[eventsignals.Action][]GeneratedEntry
	ContentCues map[eventsignals.ContentSignal][]GeneratedEntry
}

// Len is the number of phrases in g, across every bucket. Types are not
// counted.
func (g GeneratedRules) Len() int {
	n := 0
	for _, es := range g.ActionTerms {
		n += len(es)
	}
	for _, es := range g.ContentCues {
		n += len(es)
	}
	return n
}

type yamlGenerated struct {
	Meta        map[string]any              `yaml:"meta"`
	Types       []GeneratedType             `yaml:"event_types,omitempty"`
	ActionTerms map[string][]GeneratedEntry `yaml:"action_terms"`
	ContentCues map[string][]GeneratedEntry `yaml:"content_cues"`
}

// ParseGenerated reads path. A missing file is the empty set, not an error.
// It checks structure only: bucket names are checked against the
// hand-written taxonomy by LoadWithGenerated.
func ParseGenerated(path string) (GeneratedRules, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-provided config path
	if errors.Is(err, fs.ErrNotExist) {
		return GeneratedRules{}, nil
	}
	if err != nil {
		return GeneratedRules{}, fmt.Errorf("clusteringrules: read %s: %w", path, err)
	}
	var yg yamlGenerated
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&yg); err != nil && !errors.Is(err, io.EOF) {
		return GeneratedRules{}, fmt.Errorf("clusteringrules: parse %s: %w", path, err)
	}
	if err := checkTypes(path, yg.Types); err != nil {
		return GeneratedRules{}, err
	}
	g := GeneratedRules{Types: yg.Types}
	for k, entries := range yg.ActionTerms {
		a := eventsignals.Action(k)
		if err := checkEntries(path, "action_terms."+k, entries); err != nil {
			return GeneratedRules{}, err
		}
		if g.ActionTerms == nil {
			g.ActionTerms = map[eventsignals.Action][]GeneratedEntry{}
		}
		g.ActionTerms[a] = entries
	}
	for k, entries := range yg.ContentCues {
		s := eventsignals.ContentSignal(k)
		if err := checkEntries(path, "content_cues."+k, entries); err != nil {
			return GeneratedRules{}, err
		}
		if g.ContentCues == nil {
			g.ContentCues = map[eventsignals.ContentSignal][]GeneratedEntry{}
		}
		g.ContentCues[s] = entries
	}
	return g, nil
}

// checkTypes checks each generated event type's structure: a slug name,
// unique across the file, a positive window, a proposal id, and no flag
// but referential.
func checkTypes(path string, types []GeneratedType) error {
	seen := make(map[string]bool, len(types))
	for _, t := range types {
		if err := checkName("event_types", t.Name, seen[t.Name]); err != nil {
			return fmt.Errorf("clusteringrules: %s: %w", path, err)
		}
		seen[t.Name] = true
		section := fmt.Sprintf("event_types[%s]", t.Name)
		if _, err := generatedWindow(t); err != nil {
			return fmt.Errorf("clusteringrules: %s: %s: %w", path, section, err)
		}
		if strings.TrimSpace(t.ProposalID) == "" {
			return fmt.Errorf("clusteringrules: %s: %s: proposal_id is required", path, section)
		}
		for _, fl := range t.Flags {
			if eventsignals.TypeFlag(fl) != eventsignals.FlagReferential {
				return fmt.Errorf("clusteringrules: %s: %s: flag %q is not allowed (only %q)", path, section, fl, eventsignals.FlagReferential)
			}
		}
	}
	return nil
}

// generatedWindow parses t's window, which must be a positive duration.
func generatedWindow(t GeneratedType) (time.Duration, error) {
	d, err := time.ParseDuration(t.Window)
	if err != nil {
		return 0, fmt.Errorf("invalid window %q: %w", t.Window, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("window must be a positive duration, got %q", t.Window)
	}
	return d, nil
}

func checkEntries(path, section string, entries []GeneratedEntry) error {
	for _, e := range entries {
		if strings.TrimSpace(e.Phrase) == "" {
			return fmt.Errorf("clusteringrules: %s: %s: phrase must not be blank", path, section)
		}
	}
	return nil
}

// WriteGenerated writes g deterministically (types sorted by name, sorted
// buckets and phrases) and atomically (temp file + rename). It refuses a
// type with no phrase in its action_terms bucket.
func WriteGenerated(path string, g GeneratedRules) error {
	for _, t := range g.Types {
		if len(g.ActionTerms[eventsignals.Action(t.Name)]) == 0 {
			return fmt.Errorf("clusteringrules: generated event type %q has no phrase", t.Name)
		}
	}
	yg := yamlGenerated{Meta: map[string]any{"version": 1}, Types: sortedTypes(g.Types), ActionTerms: map[string][]GeneratedEntry{}, ContentCues: map[string][]GeneratedEntry{}}
	for a, es := range g.ActionTerms {
		yg.ActionTerms[string(a)] = sortedEntries(es)
	}
	for s, es := range g.ContentCues {
		yg.ContentCues[string(s)] = sortedEntries(es)
	}
	data, err := yaml.Marshal(yg) // yaml.v3 sorts map keys
	if err != nil {
		return fmt.Errorf("clusteringrules: marshal generated rules: %w", err)
	}
	header := []byte("# Generated by `editorial cycle` (event rules step). Do not edit by hand:\n# remove an entry to revert it. Hand-written rules in event_clustering_rules.yml always win.\n")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".event_rules_generated-*.yml")
	if err != nil {
		return fmt.Errorf("clusteringrules: create temp: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(header, data...)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clusteringrules: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sortedEntries(in []GeneratedEntry) []GeneratedEntry {
	out := append([]GeneratedEntry(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Phrase < out[j].Phrase })
	return out
}

func sortedTypes(in []GeneratedType) []GeneratedType {
	out := append([]GeneratedType(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MergeGenerated returns a copy of f with g's types and phrases appended.
// Each generated type adds an event type, then an action of the same name
// after every hand-written action (never body fallback), and its window.
// A phrase that already exists anywhere in the hand-written vocabulary
// (action terms or content cues, any bucket, case-insensitive) is skipped
// and reported. A type left with no phrase is kept and reported as inert.
// It assumes g passed ParseGenerated and CheckGeneratedBuckets.
func MergeGenerated(f RulesFile, g GeneratedRules) (RulesFile, []string) {
	out := f
	out.ActionTerms = cloneBuckets(f.ActionTerms)
	out.ContentCues = cloneSignalBuckets(f.ContentCues)
	types := sortedTypes(g.Types)
	if len(types) > 0 {
		out.Taxonomy.EventTypes = append([]eventsignals.EventTypeDef(nil), f.Taxonomy.EventTypes...)
		out.Taxonomy.Actions = append([]eventsignals.ActionDef(nil), f.Taxonomy.Actions...)
		out.EventWindows = make(map[eventsignals.EventType]time.Duration, len(f.EventWindows)+len(types))
		for k, v := range f.EventWindows {
			out.EventWindows[k] = v
		}
	}
	for _, t := range types {
		name := eventsignals.EventType(t.Name)
		flags := make([]eventsignals.TypeFlag, 0, len(t.Flags))
		for _, fl := range t.Flags {
			flags = append(flags, eventsignals.TypeFlag(fl))
		}
		out.Taxonomy.EventTypes = append(out.Taxonomy.EventTypes, eventsignals.EventTypeDef{Name: name, Description: t.Description, Flags: flags})
		out.Taxonomy.Actions = append(out.Taxonomy.Actions, eventsignals.ActionDef{Name: eventsignals.Action(t.Name), EventType: name})
		out.EventWindows[name], _ = generatedWindow(t) // checked by ParseGenerated
	}

	taken := map[string]string{}
	for a, ps := range f.ActionTerms {
		for _, p := range ps {
			taken[norm(p)] = "action_terms." + string(a)
		}
	}
	for s, ps := range f.ContentCues {
		for _, p := range ps {
			taken[norm(p)] = "content_cues." + string(s)
		}
	}
	var warnings []string
	add := func(bucket, phrase string, appendFn func()) {
		if where, dup := taken[norm(phrase)]; dup {
			warnings = append(warnings, fmt.Sprintf("generated phrase %q for %s skipped: already in %s", phrase, bucket, where))
			return
		}
		taken[norm(phrase)] = bucket
		appendFn()
	}
	for _, a := range sortedActions(g.ActionTerms) {
		for _, e := range sortedEntries(g.ActionTerms[a]) {
			add("action_terms."+string(a), e.Phrase, func() { out.ActionTerms[a] = append(out.ActionTerms[a], e.Phrase) })
		}
	}
	for _, s := range sortedSignals(g.ContentCues) {
		for _, e := range sortedEntries(g.ContentCues[s]) {
			add("content_cues."+string(s), e.Phrase, func() { out.ContentCues[s] = append(out.ContentCues[s], e.Phrase) })
		}
	}
	for _, t := range types {
		if len(out.ActionTerms[eventsignals.Action(t.Name)]) == 0 {
			warnings = append(warnings, fmt.Sprintf("generated event type %q has no phrase; it is inert", t.Name))
		}
	}
	return out, warnings
}

// LoadWithGenerated parses generatedPath, checks its buckets against f's
// taxonomy, merges it into f, then validates the merged set. An empty path
// is a no-op (the feature is off).
func LoadWithGenerated(f RulesFile, generatedPath string) (RulesFile, []string, error) {
	if generatedPath == "" {
		return f, nil, nil
	}
	g, err := ParseGenerated(generatedPath)
	if err != nil {
		return RulesFile{}, nil, err
	}
	if err := CheckGeneratedBuckets(generatedPath, g, f.Taxonomy); err != nil {
		return RulesFile{}, nil, err
	}
	merged, warnings := MergeGenerated(f, g)
	if err := merged.Validate(); err != nil {
		return RulesFile{}, nil, fmt.Errorf("clusteringrules: merged generated rules from %s: %w", generatedPath, err)
	}
	return merged, warnings, nil
}

// CheckGeneratedBuckets rejects a generated type whose name the
// hand-written taxonomy already declares (as a type or an action), and a
// bucket of g (read from path) naming an action or content signal that
// neither the taxonomy nor g's types declare.
func CheckGeneratedBuckets(path string, g GeneratedRules, taxonomy eventsignals.Taxonomy) error {
	actions := declaredActions(taxonomy)
	handTypes := make(map[eventsignals.EventType]bool, len(taxonomy.EventTypes))
	for _, et := range taxonomy.EventTypes {
		handTypes[et.Name] = true
	}
	for _, t := range sortedTypes(g.Types) {
		if handTypes[eventsignals.EventType(t.Name)] || actions[eventsignals.Action(t.Name)] {
			return fmt.Errorf("clusteringrules: %s: generated event type %q is already declared in the hand-written taxonomy", path, t.Name)
		}
	}
	for _, t := range g.Types {
		actions[eventsignals.Action(t.Name)] = true
	}
	for _, a := range sortedActions(g.ActionTerms) {
		if !actions[a] {
			return fmt.Errorf("clusteringrules: %s: unknown action %q", path, a)
		}
	}
	signals := declaredSignals(taxonomy)
	for _, s := range sortedSignals(g.ContentCues) {
		if !signals[s] {
			return fmt.Errorf("clusteringrules: %s: unknown content signal %q", path, s)
		}
	}
	return nil
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func cloneBuckets(in map[eventsignals.Action][]string) map[eventsignals.Action][]string {
	out := make(map[eventsignals.Action][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneSignalBuckets(in map[eventsignals.ContentSignal][]string) map[eventsignals.ContentSignal][]string {
	out := make(map[eventsignals.ContentSignal][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func sortedActions(m map[eventsignals.Action][]GeneratedEntry) []eventsignals.Action {
	ks := make([]eventsignals.Action, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

func sortedSignals(m map[eventsignals.ContentSignal][]GeneratedEntry) []eventsignals.ContentSignal {
	ks := make([]eventsignals.ContentSignal, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
