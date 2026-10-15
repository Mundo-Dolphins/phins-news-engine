// Package editorialveto decodes a veto-rules YAML file into
// domain/editorialveto's types — the YAML shape is kept separate from
// the domain types, same pattern infrastructure/dictionaryfile already
// uses for the team dictionary.
package editorialveto

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

type yamlRule struct {
	Category        string   `yaml:"category"`
	ReasonCode      string   `yaml:"reason_code"`
	Patterns        []string `yaml:"patterns"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
}

type yamlFile struct {
	Version string     `yaml:"version"`
	Rules   []yamlRule `yaml:"rules"`
}

// Load reads path and decodes it into an editorialveto.Config, failing
// closed on any unknown YAML field or invalid regex pattern — a
// misconfigured veto file must never load partially/silently, since it
// governs a moderation decision.
func Load(path string) (editorialveto.Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return editorialveto.Config{}, fmt.Errorf("editorialveto: read %s: %w", path, err)
	}

	var f yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return editorialveto.Config{}, fmt.Errorf("editorialveto: parse %s: %w", path, err)
	}

	rules := make([]editorialveto.Rule, 0, len(f.Rules))
	for i, r := range f.Rules {
		for _, p := range append(append([]string{}, r.Patterns...), r.ExcludePatterns...) {
			if _, err := regexp.Compile(p); err != nil {
				return editorialveto.Config{}, fmt.Errorf("editorialveto: %s: rule %d pattern %q: %w", path, i, p, err)
			}
		}
		rules = append(rules, editorialveto.Rule{
			Category: editorialveto.Category(r.Category), ReasonCode: r.ReasonCode, Patterns: r.Patterns, ExcludePatterns: r.ExcludePatterns,
		})
	}

	return editorialveto.Config{Version: f.Version, Rules: rules}, nil
}
