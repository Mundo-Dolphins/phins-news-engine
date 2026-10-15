// Package entitycatalog loads the entity catalog eventsignals.Rules.Entities
// needs from two YAML files: a small, hand-authored manual file (teams,
// competitions) and a large, machine-generated file (Isotopes players/
// coaches/executives, produced by `phins-news-engine-tools entities
// generate` — see app/entitycatalogbuild). Neither this
// package nor eventsignals ever imports domain/dictionary; only
// the generator command does.
package entitycatalog

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

type yamlEntityRule struct {
	ID            string   `yaml:"id"`
	CanonicalName string   `yaml:"canonical_name"`
	Kind          string   `yaml:"kind"`
	Terms         []string `yaml:"terms"`
	IsPrimaryTeam bool     `yaml:"is_primary_team"`
	Position      string   `yaml:"position"`
	TeamID        string   `yaml:"team_id,omitempty"`
	// AffiliationStatus is team | free_agent | unknown; absent on legacy
	// entries (see eventsignals.EntityRule.EffectiveAffiliation).
	AffiliationStatus string `yaml:"affiliation_status,omitempty"`
	Role              string `yaml:"role,omitempty"`
}

type yamlFile struct {
	Entities []yamlEntityRule `yaml:"entities"`
}

var knownKinds = map[eventsignals.EntityKind]bool{
	eventsignals.EntityKindPlayer:      true,
	eventsignals.EntityKindCoach:       true,
	eventsignals.EntityKindExecutive:   true,
	eventsignals.EntityKindTeam:        true,
	eventsignals.EntityKindCompetition: true,
}

// Load reads and merges manualPath and generatedPath into entity rules
// for eventsignals.Rules.Entities. Returns an error on a missing id, an
// unrecognized kind, an entity with no terms, a duplicate id across (or
// within) the two files, an empty canonical name, a duplicate canonical
// name across (or within) the two files, IsPrimaryTeam set on a
// non-EntityKindTeam entity, or more than one entity with
// IsPrimaryTeam == true.
func Load(manualPath, generatedPath string) ([]eventsignals.EntityRule, error) {
	manual, err := parseFile(manualPath)
	if err != nil {
		return nil, fmt.Errorf("entitycatalog: %w", err)
	}
	generated, err := parseFile(generatedPath)
	if err != nil {
		return nil, fmt.Errorf("entitycatalog: %w", err)
	}

	all := make([]eventsignals.EntityRule, 0, len(manual)+len(generated))
	all = append(all, manual...)
	all = append(all, generated...)

	seen := make(map[string]string, len(all))
	seenCanonical := make(map[string]string, len(all))
	primaryTeamCount := 0
	for i, e := range all {
		if e.ID == "" {
			return nil, fmt.Errorf("entitycatalog: entity %q (kind %s) is missing an id", e.CanonicalName, e.Kind)
		}
		if !knownKinds[e.Kind] {
			return nil, fmt.Errorf("entitycatalog: entity %q: unrecognized kind %q", e.CanonicalName, e.Kind)
		}
		if len(e.Terms) == 0 {
			return nil, fmt.Errorf("entitycatalog: entity %q: must have at least one term", e.CanonicalName)
		}
		if e.CanonicalName == "" {
			return nil, fmt.Errorf("entitycatalog: entity %q: missing a canonical name", e.ID)
		}
		if e.IsPrimaryTeam && e.Kind != eventsignals.EntityKindTeam {
			return nil, fmt.Errorf("entitycatalog: entity %q: is_primary_team is set but kind is %q, not %q", e.ID, e.Kind, eventsignals.EntityKindTeam)
		}
		if e.IsPrimaryTeam {
			primaryTeamCount++
		}
		src := generatedPath
		if i < len(manual) {
			src = manualPath
		}
		if prevSrc, ok := seen[e.ID]; ok {
			return nil, fmt.Errorf("entitycatalog: duplicate id %q (defined in both %s and %s)", e.ID, prevSrc, src)
		}
		seen[e.ID] = src
		if prevSrc, ok := seenCanonical[e.CanonicalName]; ok {
			return nil, fmt.Errorf("entitycatalog: duplicate canonical name %q (defined in both %s and %s)", e.CanonicalName, prevSrc, src)
		}
		seenCanonical[e.CanonicalName] = src
	}
	kindByID := make(map[string]eventsignals.EntityKind, len(all))
	for _, e := range all {
		kindByID[e.ID] = e.Kind
	}
	for _, e := range all {
		if err := eventsignals.ValidateAffiliation(e.Kind, e.AffiliationStatus, e.TeamID, ""); err != nil {
			return nil, fmt.Errorf("entitycatalog: entity %q: %w", e.ID, err)
		}
		if e.TeamID == "" {
			continue
		}
		k, ok := kindByID[e.TeamID]
		if !ok {
			return nil, fmt.Errorf("entitycatalog: entity %q: team_id %q does not reference a known entity", e.ID, e.TeamID)
		}
		if k != eventsignals.EntityKindTeam {
			return nil, fmt.Errorf("entitycatalog: entity %q: team_id %q references a %q entity, not a team", e.ID, e.TeamID, k)
		}
	}
	if primaryTeamCount > 1 {
		return nil, fmt.Errorf("entitycatalog: more than one entity has is_primary_team set (found %d)", primaryTeamCount)
	}
	return all, nil
}

func parseFile(path string) ([]eventsignals.EntityRule, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var yf yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&yf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	rules := make([]eventsignals.EntityRule, 0, len(yf.Entities))
	for _, e := range yf.Entities {
		rule := eventsignals.EntityRule{
			ID:            e.ID,
			CanonicalName: e.CanonicalName,
			Kind:          eventsignals.EntityKind(e.Kind),
			Terms:         e.Terms,
			IsPrimaryTeam: e.IsPrimaryTeam,
			Position:      e.Position,
			TeamID:        e.TeamID,
			Role:          e.Role,
		}
		status, err := eventsignals.ParseAffiliationStatus(e.AffiliationStatus)
		if err != nil {
			return nil, fmt.Errorf("parse %s: entity %q: %w", path, e.ID, err)
		}
		rule.AffiliationStatus = status
		rules = append(rules, rule)
	}
	return rules, nil
}

// WriteGenerated writes entries to path as the YAML shape Load reads,
// headed by a do-not-edit warning. Used only by the `entities generate`
// CLI command (app/entitycatalogbuild's caller).
func WriteGenerated(path string, entries []eventsignals.EntityRule) error {
	yf := yamlFile{Entities: make([]yamlEntityRule, 0, len(entries))}
	for _, e := range entries {
		yf.Entities = append(yf.Entities, toYAML(e))
	}
	out, err := yaml.Marshal(yf)
	if err != nil {
		return fmt.Errorf("entitycatalog: marshal %s: %w", path, err)
	}
	header := "# GENERATED FILE — DO NOT EDIT BY HAND.\n" +
		"# Produced by the tools command `entities generate` from the promoted\n" +
		"# team dictionary. Re-run that command after a roster change.\n\n"
	if err := os.WriteFile(path, append([]byte(header), out...), 0o600); err != nil {
		return fmt.Errorf("entitycatalog: write %s: %w", path, err)
	}
	return nil
}

// LoadOne reads a single entity-catalog YAML file and returns its
// entities without merging against a second file or checking for
// cross-file id/name collisions — unlike Load, which is only for the
// startup manual+generated merge. Used by `entities discover`/`entities
// approve`, which read and write the manual catalog file on
// its own.
func LoadOne(path string) ([]eventsignals.EntityRule, error) {
	rules, err := parseFile(path)
	if err != nil {
		return nil, fmt.Errorf("entitycatalog: %w", err)
	}
	return rules, nil
}

// WriteManual writes entries to path in the same YAML shape Load reads,
// with no generated-file warning header (unlike WriteGenerated) — manual
// catalog files are meant to be hand-edited/reviewed after this write.
// Used by `entities approve` when converting an approved team proposal,
// or a new alias for an existing manual-catalog entity, into a real
// entry.
func WriteManual(path string, entries []eventsignals.EntityRule) error {
	yf := yamlFile{Entities: make([]yamlEntityRule, 0, len(entries))}
	for _, e := range entries {
		yf.Entities = append(yf.Entities, toYAML(e))
	}
	out, err := yaml.Marshal(yf)
	if err != nil {
		return fmt.Errorf("entitycatalog: marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("entitycatalog: write %s: %w", path, err)
	}
	return nil
}

func toYAML(e eventsignals.EntityRule) yamlEntityRule {
	return yamlEntityRule{
		ID: e.ID, CanonicalName: e.CanonicalName, Kind: string(e.Kind),
		Terms: e.Terms, IsPrimaryTeam: e.IsPrimaryTeam, Position: e.Position,
		TeamID: e.TeamID, Role: e.Role, AffiliationStatus: string(e.AffiliationStatus),
	}
}
