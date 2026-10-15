package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// TestSettingsYAMLMatchesConfigSchema is a structural drift guard between
// configs/example/settings.yml (the shipped example) and config.Config's yaml
// struct tags. It checks both directions:
//   - every struct leaf field must appear somewhere in settings.yml
//     (catches a new/renamed field nobody added an example for — Load's
//     strict decoding does not catch this direction, only the reverse);
//   - every settings.yml key must map to a struct field or an ancestor
//     of one (belt-and-suspenders; Load's dec.KnownFields(true) already
//     guarantees this at load time, exercised indirectly by `make
//     validate-config` in CI, but cheap to assert directly here too).
//
// Map-typed and slice-typed struct fields (e.g. scraping_service.domains,
// dictionary_update.sources, classification.trusted_source_ids) are
// treated as single leaves, not recursed into — their YAML contents are
// operator data (arbitrary keys/values), not part of the schema.
func TestSettingsYAMLMatchesConfigSchema(t *testing.T) {
	structLeaves := make(map[string]bool)
	collectStructLeaves(reflect.TypeOf(config.Config{}), "", structLeaves)
	if len(structLeaves) == 0 {
		t.Fatal("collectStructLeaves found no yaml-tagged fields — walker is broken")
	}
	validPaths := expandPrefixes(structLeaves)

	data, err := os.ReadFile(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatalf("read settings.yml: %v", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse settings.yml: %v", err)
	}
	found := make(map[string]bool)
	collectYAMLPaths(raw, "", structLeaves, found)

	for leaf := range structLeaves {
		if !found[leaf] {
			t.Errorf("config.Config field %q has no example in settings.yml — add one", leaf)
		}
	}
	for p := range found {
		if !validPaths[p] {
			t.Errorf("settings.yml key %q has no matching config.Config field", p)
		}
	}
}

// collectStructLeaves recursively walks typ's fields, recording the
// dotted yaml path of every leaf (scalar, slice, or map field — anything
// that isn't itself a nested struct) into out. Fields tagged yaml:"-"
// (secrets) are skipped entirely, since they are never in YAML by
// design.
func collectStructLeaves(typ reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct {
			collectStructLeaves(f.Type, path, out)
			continue
		}
		out[path] = true
	}
}

// expandPrefixes returns leaves plus every proper ancestor path of each
// leaf (e.g. "scraping_service.domains" also contributes
// "scraping_service") — the full set of paths that are "known" to the
// schema, whether as a leaf or as a container on the way to one.
func expandPrefixes(leaves map[string]bool) map[string]bool {
	all := make(map[string]bool)
	for leaf := range leaves {
		parts := strings.Split(leaf, ".")
		for i := 1; i <= len(parts); i++ {
			all[strings.Join(parts[:i], ".")] = true
		}
	}
	return all
}

// collectYAMLPaths recursively walks a generically-parsed YAML mapping,
// recording every path it visits into found. Recursion stops the moment
// a path matches a known struct leaf (structLeaves) — deeper keys under
// a map-typed config field (e.g. scraping_service.domains.<host>.mode)
// are operator data, not schema, and must not be compared individually.
func collectYAMLPaths(m map[string]any, prefix string, structLeaves, found map[string]bool) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		found[path] = true
		if structLeaves[path] {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			collectYAMLPaths(nested, path, structLeaves, found)
		}
	}
}
