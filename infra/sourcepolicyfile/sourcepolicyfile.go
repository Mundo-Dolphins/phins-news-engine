// Package sourcepolicyfile loads and validates configs/source-policies.yml
// and resolves per-source sourcepolicy.Policy values for the application
// layer. Mirrors infra/sourcesfile's conventions exactly
// (strict YAML, Validate() rejecting unknown/invalid data) but is a
// separate file from sources.yml — policy tuning changes far more often
// than connection config, and embedding it per-platform in sourcesfile.go
// would duplicate the same block across 5 structs.
package sourcepolicyfile

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

// PolicyEntry is one source's raw configured policy. exact_duplicates is
// parsed and validated but never stored anywhere the application layer
// can read — it is not part of sourcepolicy.Policy at all (see that
// type's doc comment): if present it must be the literal string "reject",
// otherwise Validate rejects the whole file.
type PolicyEntry struct {
	SourceID                                 source.ID `yaml:"source_id"`
	Authority                                string    `yaml:"authority"`
	ExactDuplicates                          string    `yaml:"exact_duplicates"`
	RetainNearDuplicates                     bool      `yaml:"retain_near_duplicates"`
	RepresentativePreference                 string    `yaml:"representative_preference"`
	AllowOfficialConfirmation                bool      `yaml:"allow_official_confirmation"`
	AllowSubstantialUpdate                   bool      `yaml:"allow_substantial_update"`
	PublishConfirmationWithoutNewInformation bool      `yaml:"publish_confirmation_without_new_information"`
	MinimumUpdateNovelty                     float64   `yaml:"minimum_update_novelty"`
	EventCooldown                            string    `yaml:"event_cooldown"`
}

// EventCooldownDuration parses EventCooldown ("20m", "1h", ...) — "" means
// no cooldown (0). Validate has already guaranteed this parses cleanly by
// the time any caller reaches this method.
func (p PolicyEntry) EventCooldownDuration() time.Duration {
	if p.EventCooldown == "" {
		return 0
	}
	d, _ := time.ParseDuration(p.EventCooldown) // validated in Validate()
	return d
}

// File is the fully loaded and validated configs/source-policies.yml.
type File struct {
	Version  int           `yaml:"version"`
	Mode     string        `yaml:"mode"`
	Policies []PolicyEntry `yaml:"policies"`
}

// Validate checks File for structural and semantic correctness: valid
// authority levels, valid representative-preference tiers, no duplicate
// source_id, exact_duplicates (if present) is literally "reject",
// minimum_update_novelty in [0,1], event_cooldown parses as a duration,
// and mode is one of the three known values (empty is normalized to
// "disabled" by Load before Validate runs, so Validate itself never sees
// "").
func (f File) Validate() error {
	if !sourcepolicy.Mode(f.Mode).Valid() {
		return fmt.Errorf("sourcepolicyfile: invalid mode %q", f.Mode)
	}
	seen := make(map[string]bool, len(f.Policies))
	for _, p := range f.Policies {
		if p.SourceID == (source.ID{}) {
			return fmt.Errorf("sourcepolicyfile: policies: source_id is required")
		}
		if seen[p.SourceID.String()] {
			return fmt.Errorf("sourcepolicyfile: policies: duplicate source_id %q", p.SourceID)
		}
		seen[p.SourceID.String()] = true

		if !sourcepolicy.AuthorityLevel(p.Authority).Valid() {
			return fmt.Errorf("sourcepolicyfile: policies[%s]: invalid authority %q", p.SourceID, p.Authority)
		}
		if p.RepresentativePreference != "" && !sourcepolicy.RepresentativePreference(p.RepresentativePreference).Valid() {
			return fmt.Errorf("sourcepolicyfile: policies[%s]: invalid representative_preference %q", p.SourceID, p.RepresentativePreference)
		}
		if p.ExactDuplicates != "" && p.ExactDuplicates != "reject" {
			return fmt.Errorf("sourcepolicyfile: policies[%s]: exact_duplicates must be \"reject\" if set, got %q", p.SourceID, p.ExactDuplicates)
		}
		if p.MinimumUpdateNovelty < 0 || p.MinimumUpdateNovelty > 1 {
			return fmt.Errorf("sourcepolicyfile: policies[%s]: minimum_update_novelty must be in [0,1], got %v", p.SourceID, p.MinimumUpdateNovelty)
		}
		if p.EventCooldown != "" {
			if _, err := time.ParseDuration(p.EventCooldown); err != nil {
				return fmt.Errorf("sourcepolicyfile: policies[%s]: invalid event_cooldown %q: %w", p.SourceID, p.EventCooldown, err)
			}
		}
	}
	return nil
}

// Load reads path, decodes it strictly (unknown keys rejected), defaults
// an empty mode to "disabled", validates the result, and returns it.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI/config value, not user input
	if err != nil {
		return nil, fmt.Errorf("sourcepolicyfile: read %s: %w", path, err)
	}

	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("sourcepolicyfile: parse %s: %w", path, err)
	}
	if f.Mode == "" {
		f.Mode = string(sourcepolicy.ModeDisabled)
	}

	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}
