// Package dictionaryminingfile loads the operator-maintained noise policy
// used by dictionary candidate mining.
package dictionaryminingfile

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
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

// LoadEnglishWords reads a newline-separated word list (one word per line, any
// case) into a lower-case set. Entries with spaces or digits are ignored.
func LoadEnglishWords(path string) (map[string]bool, error) {
	f, err := os.Open(path) // #nosec G304 -- path is operator-provided config
	if err != nil {
		return nil, fmt.Errorf("dictionaryminingfile: open %s: %w", path, err)
	}
	defer f.Close()
	words := make(map[string]bool, 250000)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		w := strings.ToLower(strings.TrimSpace(sc.Text()))
		if w == "" || strings.ContainsAny(w, " \t0123456789") {
			continue
		}
		words[w] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dictionaryminingfile: read %s: %w", path, err)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("dictionaryminingfile: %s holds no words", path)
	}
	return words, nil
}

// NamesFromRoster returns the full names and the surnames of a nflverse
// roster, in the form dictionarymining.FilterConfig.KnownNames and
// KnownSurnames expect, so a person the roster already covers is not mined.
func NamesFromRoster(players []nflverse.Player) (names, surnames map[string]bool) {
	names = make(map[string]bool, len(players)*2)
	surnames = make(map[string]bool, len(players))
	for _, p := range players {
		if k := dictionarymining.NameKey(p.FullName); strings.Contains(k, " ") {
			names[k] = true
		}
		if p.FootballName != "" && p.LastName != "" {
			if k := dictionarymining.NameKey(p.FootballName + " " + p.LastName); strings.Contains(k, " ") {
				names[k] = true
			}
		}
		if k := dictionarymining.NameKey(p.LastName); k != "" && !strings.Contains(k, " ") && len(k) >= 4 {
			surnames[k] = true
		}
	}
	return names, surnames
}

// AddIgnoredTerm appends term to the ignored_terms list of the mining policy
// file at path, so a term the operator rejected is never proposed again by any
// later run, on any machine that shares the file. It edits the text in place
// (comments and ordering are kept), does nothing when the term is already
// listed, and returns whether it wrote.
func AddIgnoredTerm(path, term string) (bool, error) {
	key := strings.Join(strings.Fields(strings.ToLower(term)), " ")
	if key == "" {
		return false, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is operator-provided config
	if err != nil {
		return false, fmt.Errorf("dictionaryminingfile: read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "ignored_terms:" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			start = i
			break
		}
	}
	if start < 0 {
		return false, fmt.Errorf("dictionaryminingfile: %s has no ignored_terms list", path)
	}
	end := start // index of the last list line
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(t, "- "):
			existing := strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "- ")), "\"'")
			if strings.Join(strings.Fields(strings.ToLower(existing)), " ") == key {
				return false, nil
			}
			end = i
		case t == "" || strings.HasPrefix(t, "#"):
			// blank lines and comments inside the list
		default:
			i = len(lines)
		}
	}
	entry := "  - " + key
	if strings.ContainsAny(key, ":#'\"") {
		entry = "  - \"" + strings.ReplaceAll(key, "\"", "\\\"") + "\""
	}
	lines = append(lines[:end+1], append([]string{entry}, lines[end+1:]...)...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return false, fmt.Errorf("dictionaryminingfile: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("dictionaryminingfile: replace %s: %w", path, err)
	}
	return true, nil
}
