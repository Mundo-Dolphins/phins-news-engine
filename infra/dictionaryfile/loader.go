// Package dictionaryfile decodes a dictionary YAML file into
// domain/dictionary's types. The YAML shape (yamlEntry etc.) is
// kept separate from the domain types — same pattern
// infra/config already uses — so the domain package
// never carries yaml struct tags.
package dictionaryfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

type yamlMeta struct {
	Version       string   `yaml:"version"`
	Name          string   `yaml:"name"`
	Language      string   `yaml:"language"`
	GeneratedAt   string   `yaml:"generated_at"`
	SchemaVersion int      `yaml:"schema_version,omitempty"`
	Checksum      string   `yaml:"checksum,omitempty"`
	Sources       []string `yaml:"sources,omitempty"`
	SnapshotRef   string   `yaml:"snapshot_ref,omitempty"`
	ChangeCount   int      `yaml:"change_count,omitempty"`
	Tool          string   `yaml:"tool,omitempty"`
}

type yamlEntry struct {
	CanonicalName string   `yaml:"canonical_name"`
	Aliases       []string `yaml:"aliases,omitempty"`
	Type          string   `yaml:"type"`
	RosterStatus  string   `yaml:"roster_status,omitempty"`
	Role          string   `yaml:"role,omitempty"`
	Position      string   `yaml:"position,omitempty"`
	Number        string   `yaml:"number,omitempty"`
	ActiveSince   string   `yaml:"active_since"`
	ActiveUntil   string   `yaml:"active_until"`
	Source        string   `yaml:"source"`
	UpdatedAt     string   `yaml:"updated_at"`
	Confidence    float64  `yaml:"confidence"`
	Notes         string   `yaml:"notes"`
}

type yamlCategory struct {
	Weight         float64     `yaml:"weight"`
	StrongOverride bool        `yaml:"strong_override"`
	RequiresAnyOf  []string    `yaml:"requires_any_of,omitempty"`
	Entries        []yamlEntry `yaml:"entries"`
}

type yamlDecision struct {
	PositiveThresholdHigh   float64 `yaml:"positive_threshold_high"`
	PositiveThresholdMedium float64 `yaml:"positive_threshold_medium"`
	NegativeThresholdHigh   float64 `yaml:"negative_threshold_high"`
	TitleWeightMultiplier   float64 `yaml:"title_weight_multiplier"`
	MaxCountPerTerm         int     `yaml:"max_count_per_term"`
}

type yamlFile struct {
	Meta       yamlMeta                `yaml:"meta"`
	Decision   yamlDecision            `yaml:"decision"`
	Categories map[string]yamlCategory `yaml:"categories,omitempty"`
	Includes   []string                `yaml:"includes,omitempty"`
}

// yamlFragment is a dictionary component. Components deliberately only own
// categories: versioning and scoring policy stay in the root dictionary file.
type yamlFragment struct {
	Categories map[string]yamlCategory `yaml:"categories"`
}

// Load reads path and decodes it into a dictionary.Dictionary. It only
// parses — it never calls dictionary.Validate, so callers can choose
// fail-fast (main.go, on service startup) or report-all (the `dictionary
// validate` CLI command) behavior on top of the same decode step.
func Load(path string) (dictionary.Dictionary, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return dictionary.Dictionary{}, fmt.Errorf("dictionaryfile: read %s: %w", path, err)
	}

	f, err := decodeFile(data, path)
	if err != nil {
		return dictionary.Dictionary{}, err
	}

	if err := loadIncludedCategories(path, &f); err != nil {
		return dictionary.Dictionary{}, err
	}

	d, err := toDictionary(f)
	if err != nil {
		return dictionary.Dictionary{}, fmt.Errorf("dictionaryfile: %s: %w", path, err)
	}
	return d, nil
}

// IncludePaths returns the files a root dictionary includes, as paths
// (resolved the way Load resolves them), or nil when the dictionary has no
// includes. Saving a dictionary rewrites those files, not the root alone, so a
// caller that has to know which files an update can touch needs them.
func IncludePaths(path string) ([]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-provided dictionary path
	if err != nil {
		return nil, fmt.Errorf("dictionaryfile: read %s: %w", path, err)
	}
	root, err := decodeFile(data, path)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(root.Includes))
	for _, include := range root.Includes {
		p, err := resolveInclude(path, include)
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func decodeFile(data []byte, path string) (yamlFile, error) {
	var f yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return yamlFile{}, fmt.Errorf("dictionaryfile: parse %s: %w", path, err)
	}
	return f, nil
}

func loadIncludedCategories(path string, root *yamlFile) error {
	if len(root.Includes) == 0 {
		return nil
	}
	if len(root.Categories) != 0 {
		return fmt.Errorf("dictionaryfile: %s: categories and includes cannot be used together", path)
	}

	root.Categories = make(map[string]yamlCategory)
	seenFiles := make(map[string]struct{}, len(root.Includes))
	for _, include := range root.Includes {
		includePath, err := resolveInclude(path, include)
		if err != nil {
			return err
		}
		if _, ok := seenFiles[includePath]; ok {
			return fmt.Errorf("dictionaryfile: %s: duplicate include %q", path, include)
		}
		seenFiles[includePath] = struct{}{}

		data, err := os.ReadFile(includePath) // #nosec G304 -- include paths are operator-authored dictionary configuration
		if err != nil {
			return fmt.Errorf("dictionaryfile: read included file %s: %w", includePath, err)
		}
		var fragment yamlFragment
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&fragment); err != nil {
			return fmt.Errorf("dictionaryfile: parse included file %s: %w", includePath, err)
		}
		for name, category := range fragment.Categories {
			if _, exists := root.Categories[name]; exists {
				return fmt.Errorf("dictionaryfile: category %q is declared more than once", name)
			}
			root.Categories[name] = category
		}
	}
	return nil
}

func resolveInclude(rootPath, include string) (string, error) {
	if include == "" || filepath.IsAbs(include) {
		return "", fmt.Errorf("dictionaryfile: %s: include %q must be a non-empty relative path", rootPath, include)
	}
	clean := filepath.Clean(include)
	if clean == "." || clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("dictionaryfile: %s: include %q escapes the dictionary directory", rootPath, include)
	}
	return filepath.Join(filepath.Dir(rootPath), clean), nil
}

func toDictionary(f yamlFile) (dictionary.Dictionary, error) {
	generatedAt, err := parseOptionalDate(f.Meta.GeneratedAt)
	if err != nil {
		return dictionary.Dictionary{}, fmt.Errorf("meta.generated_at: %w", err)
	}

	names := make([]string, 0, len(f.Categories))
	for name := range f.Categories {
		names = append(names, name)
	}
	sort.Strings(names)

	categories := make([]dictionary.Category, 0, len(names))
	for _, name := range names {
		yc := f.Categories[name]
		entries := make([]dictionary.Entry, 0, len(yc.Entries))
		for _, ye := range yc.Entries {
			entry, err := toEntry(ye)
			if err != nil {
				return dictionary.Dictionary{}, fmt.Errorf("category %s: %w", name, err)
			}
			entries = append(entries, entry)
		}
		categories = append(categories, dictionary.Category{
			Name: name, Weight: yc.Weight, StrongOverride: yc.StrongOverride,
			RequiresAnyOf: yc.RequiresAnyOf, Entries: entries,
		})
	}

	return dictionary.Dictionary{
		Version: f.Meta.Version, Name: f.Meta.Name, Language: f.Meta.Language, GeneratedAt: timeOrZero(generatedAt),
		SchemaVersion: f.Meta.SchemaVersion, Checksum: f.Meta.Checksum, Sources: f.Meta.Sources,
		SnapshotRef: f.Meta.SnapshotRef, ChangeCount: f.Meta.ChangeCount, Tool: f.Meta.Tool,
		Categories: categories,
		Decision: dictionary.Decision{
			PositiveThresholdHigh: f.Decision.PositiveThresholdHigh, PositiveThresholdMedium: f.Decision.PositiveThresholdMedium,
			NegativeThresholdHigh: f.Decision.NegativeThresholdHigh, TitleWeightMultiplier: f.Decision.TitleWeightMultiplier,
			MaxCountPerTerm: f.Decision.MaxCountPerTerm,
		},
	}, nil
}

func toEntry(ye yamlEntry) (dictionary.Entry, error) {
	activeSince, err := parseOptionalDate(ye.ActiveSince)
	if err != nil {
		return dictionary.Entry{}, fmt.Errorf("entry %s: active_since: %w", ye.CanonicalName, err)
	}
	activeUntil, err := parseOptionalDate(ye.ActiveUntil)
	if err != nil {
		return dictionary.Entry{}, fmt.Errorf("entry %s: active_until: %w", ye.CanonicalName, err)
	}
	updatedAt, err := parseOptionalDate(ye.UpdatedAt)
	if err != nil {
		return dictionary.Entry{}, fmt.Errorf("entry %s: updated_at: %w", ye.CanonicalName, err)
	}
	return dictionary.Entry{
		CanonicalName: ye.CanonicalName, Aliases: ye.Aliases, Type: dictionary.EntryType(ye.Type),
		RosterStatus: dictionary.RosterStatus(ye.RosterStatus), Role: ye.Role, Position: ye.Position, Number: ye.Number,
		ActiveSince: activeSince, ActiveUntil: activeUntil, Source: ye.Source, UpdatedAt: timeOrZero(updatedAt),
		Confidence: ye.Confidence, Notes: ye.Notes,
	}, nil
}

func parseOptionalDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q, want YYYY-MM-DD: %w", s, err)
	}
	return &t, nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Save writes d to path in the same YAML shape Load reads. A flat dictionary
// is written atomically as one file. For a root dictionary with includes,
// categories are written back to their existing component files and the root
// metadata is written last. Category ordering in the written file is
// alphabetical (map marshaling order), which may differ from a
// hand-authored file's curated ordering — Load already reconstructs
// Dictionary.Categories in alphabetical order regardless of file order
// (see toDictionary's sort.Strings(names)), so a round trip through
// Load(Save(d)) is stable even though the very first Save of today's
// hand-curated configs/dictionary.yml will reorder its
// categories alphabetically. This is an accepted characteristic, not a
// regression: category order has no effect on classification.Decide
// (categories are looked up by name, never iterated in file order for
// scoring).
func Save(path string, d dictionary.Dictionary) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value
	if err == nil {
		root, decodeErr := decodeFile(data, path)
		if decodeErr != nil {
			return decodeErr
		}
		if len(root.Includes) > 0 {
			return saveIncluded(path, root.Includes, d)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("dictionaryfile: read %s before save: %w", path, err)
	}
	return saveFile(path, fromDictionary(d))
}

func saveIncluded(path string, includes []string, d dictionary.Dictionary) error {
	byName := make(map[string]yamlCategory, len(d.Categories))
	for _, category := range d.Categories {
		byName[category.Name] = fromCategory(category)
	}

	written := make(map[string]struct{}, len(byName))
	for _, include := range includes {
		includePath, err := resolveInclude(path, include)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(includePath) // #nosec G304 -- include paths are operator-authored dictionary configuration
		if err != nil {
			return fmt.Errorf("dictionaryfile: read included file %s before save: %w", includePath, err)
		}
		var old yamlFragment
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&old); err != nil {
			return fmt.Errorf("dictionaryfile: parse included file %s before save: %w", includePath, err)
		}
		fragment := yamlFragment{Categories: make(map[string]yamlCategory, len(old.Categories))}
		for name := range old.Categories {
			category, ok := byName[name]
			if !ok {
				return fmt.Errorf("dictionaryfile: category %q from %s is absent from dictionary being saved", name, includePath)
			}
			fragment.Categories[name] = category
			written[name] = struct{}{}
		}
		if err := saveFile(includePath, fragment); err != nil {
			return err
		}
	}
	for name := range byName {
		if _, ok := written[name]; !ok {
			return fmt.Errorf("dictionaryfile: category %q has no included file", name)
		}
	}
	flat := fromDictionary(d)
	return saveFile(path, yamlFile{Meta: flat.Meta, Decision: flat.Decision, Includes: includes})
}

func saveFile(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("dictionaryfile: marshal %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dictionary-*.tmp")
	if err != nil {
		return fmt.Errorf("dictionaryfile: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("dictionaryfile: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("dictionaryfile: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("dictionaryfile: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("dictionaryfile: rename temp file: %w", err)
	}
	return nil
}

func fromDictionary(d dictionary.Dictionary) yamlFile {
	categories := make(map[string]yamlCategory, len(d.Categories))
	for _, cat := range d.Categories {
		categories[cat.Name] = fromCategory(cat)
	}
	return yamlFile{
		Meta: yamlMeta{
			Version: d.Version, Name: d.Name, Language: d.Language, GeneratedAt: formatDate(d.GeneratedAt),
			SchemaVersion: d.SchemaVersion, Checksum: d.Checksum, Sources: d.Sources,
			SnapshotRef: d.SnapshotRef, ChangeCount: d.ChangeCount, Tool: d.Tool,
		},
		Decision: yamlDecision{
			PositiveThresholdHigh: d.Decision.PositiveThresholdHigh, PositiveThresholdMedium: d.Decision.PositiveThresholdMedium,
			NegativeThresholdHigh: d.Decision.NegativeThresholdHigh, TitleWeightMultiplier: d.Decision.TitleWeightMultiplier,
			MaxCountPerTerm: d.Decision.MaxCountPerTerm,
		},
		Categories: categories,
	}
}

func fromCategory(cat dictionary.Category) yamlCategory {
	entries := make([]yamlEntry, 0, len(cat.Entries))
	for _, e := range cat.Entries {
		entries = append(entries, yamlEntry{
			CanonicalName: e.CanonicalName, Aliases: e.Aliases, Type: string(e.Type),
			RosterStatus: string(e.RosterStatus), Role: e.Role, Position: e.Position, Number: e.Number,
			ActiveSince: formatOptionalDate(e.ActiveSince), ActiveUntil: formatOptionalDate(e.ActiveUntil),
			Source: e.Source, UpdatedAt: formatDate(e.UpdatedAt), Confidence: e.Confidence, Notes: e.Notes,
		})
	}
	return yamlCategory{
		Weight: cat.Weight, StrongOverride: cat.StrongOverride, RequiresAnyOf: cat.RequiresAnyOf, Entries: entries,
	}
}

func formatOptionalDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
