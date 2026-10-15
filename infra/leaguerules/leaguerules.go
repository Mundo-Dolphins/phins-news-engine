// infra/leaguerules/leaguerules.go
package leaguerules

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
)

type yamlTeam struct {
	Abbr       string   `yaml:"abbr"`
	Name       string   `yaml:"name"`
	Aliases    []string `yaml:"aliases"`
	UpperAbbrs []string `yaml:"upper_abbrs"`
}

type yamlCorrection struct {
	Mention string `yaml:"mention"`
	GSISID  string `yaml:"gsis_id"`
}

type yamlFile struct {
	NoiseMinLetters int              `yaml:"noise_min_letters"`
	CommonWords     []string         `yaml:"common_words"`
	UndecidedTerms  []string         `yaml:"undecided_terms"`
	Teams           []yamlTeam       `yaml:"teams"`
	Corrections     []yamlCorrection `yaml:"corrections"`
}

// Load reads and strictly parses the league alias rules file.
func Load(path string) (leaguegazetteer.Rules, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return leaguegazetteer.Rules{}, fmt.Errorf("leaguerules: %w", err)
	}
	var f yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return leaguegazetteer.Rules{}, fmt.Errorf("leaguerules: parse %s: %w", path, err)
	}
	rules := leaguegazetteer.Rules{NoiseMinLetters: f.NoiseMinLetters, CommonWords: f.CommonWords, UndecidedTerms: f.UndecidedTerms}
	for _, t := range f.Teams {
		rules.Teams = append(rules.Teams, leaguegazetteer.TeamRule{Abbr: t.Abbr, Name: t.Name, Aliases: t.Aliases, UpperAbbrs: t.UpperAbbrs})
	}
	for _, c := range f.Corrections {
		rules.Corrections = append(rules.Corrections, leaguegazetteer.Correction{Mention: c.Mention, GSISID: c.GSISID})
	}
	return rules, nil
}
