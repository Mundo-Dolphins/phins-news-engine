// Package dictionaryminingfile loads the operator-maintained noise policy
// used by dictionary candidate mining.
package dictionaryminingfile

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
)

type fileConfig struct {
	MinSupport        int               `yaml:"min_support"`
	IgnoredTerms      []string          `yaml:"ignored_terms"`
	IgnoredTokens     []string          `yaml:"ignored_tokens"`
	IgnoredSubstrings []string          `yaml:"ignored_substrings"`
	CategoryGuesses   map[string]string `yaml:"category_guesses"`
}

// Load reads path using strict YAML decoding and converts it into the
// application-layer filter config.
func Load(path string) (dictionarymining.FilterConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is operator-provided config
	if err != nil {
		return dictionarymining.FilterConfig{}, fmt.Errorf("dictionaryminingfile: read %s: %w", path, err)
	}
	var cfg fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return dictionarymining.FilterConfig{}, fmt.Errorf("dictionaryminingfile: parse %s: %w", path, err)
	}
	if cfg.MinSupport < 0 {
		return dictionarymining.FilterConfig{}, fmt.Errorf("dictionaryminingfile: min_support must not be negative")
	}
	return dictionarymining.FilterConfig{
		MinSupport:        cfg.MinSupport,
		IgnoredTerms:      cfg.IgnoredTerms,
		IgnoredTokens:     cfg.IgnoredTokens,
		IgnoredSubstrings: cfg.IgnoredSubstrings,
		CategoryGuesses:   normalizeCategoryGuesses(cfg.CategoryGuesses),
	}, nil
}

func normalizeCategoryGuesses(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for term, category := range in {
		key := strings.Join(strings.Fields(strings.ToLower(term)), " ")
		if key == "" || category == "" {
			continue
		}
		out[key] = category
	}
	return out
}
