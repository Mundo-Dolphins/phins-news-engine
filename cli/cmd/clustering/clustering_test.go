package clustering_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

// clusteringTestRulesYAML is a small, structurally valid clustering rules
// fixture (see infra/clusteringrules.RulesFile.Validate)
// — not the full production configs/event_clustering_rules.yml — with just
// enough vocabulary for the fixture dataset below to produce a join.
const clusteringTestRulesYAML = `
meta:
  version: "test-1.0.0"
  name: clustering_cmd_test_rules
event_rules:
  action_terms:
    sign: ["sign", "signs", "signed"]
  content_cues: {}
  date_window_policy:
    default_window: "24h"
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {}
thresholds:
  join_threshold: 0.70
  join_margin: 0.10
  ambiguous_low: 0.30
  ambiguous_high: 0.50
  substantial_update_threshold: 0.50
  representative_switch_margin: 0.10
weights:
  entity_match: 0.40
  action_match: 0.20
  event_type_match: 0.20
  opponent_match: 0.10
  date_window_match: 0.05
  source_priority: 0.05
  novelty: 0.05
  primary_source: 0.30
  completeness: 0.20
  clickbait_penalty: -0.10
domain_priorities: {}
content_type_rules: {}
`

// clusteringTestManualEntitiesYAML and clusteringTestGeneratedEntitiesYAML
// stand in for the two real entity-catalog files (see
// infra/entitycatalog.Load), supplying just the two
// entities clusteringTestDatasetYAML's articles need to join ("Test
// Player" and "Test Team") now that RulesFile.Entities is no longer
// populated from event_rules.entities directly.
const clusteringTestManualEntitiesYAML = `
entities:
  - id: test-team
    canonical_name: "Test Team"
    kind: team
    is_primary_team: true
    terms: ["test team"]
`

const clusteringTestGeneratedEntitiesYAML = `
entities:
  - id: test-player
    canonical_name: "Test Player"
    kind: player
    terms: ["test player"]
`

const clusteringTestDatasetYAML = `
groups:
  - id: g1
    representative: a1
    articles:
      - id: a1
        title: "Test Player Signs New Deal With Test Team"
        source_id: src-1
        content_type: article
        published_at: "2026-07-01T10:00:00Z"
        expected_role: first_report
      - id: a2
        title: "Test Player Signed With Test Team, Second Outlet Reports"
        source_id: src-2
        content_type: article
        published_at: "2026-07-01T11:00:00Z"
        expected_role: confirmation
`

// writeClusteringConfigFixture writes a settings.yml whose clustering.
// rules_path and clustering.entity_catalog.{manual,generated}_path point at
// rulesPath and a pair of freshly written entity-catalog fixture files (all
// absolute paths, so the fixture never depends on the test process's
// working directory).
func writeClusteringConfigFixture(t *testing.T, dir, rulesPath string) string {
	t.Helper()
	configPath := filepath.Join(dir, "settings.yml")

	manualPath := filepath.Join(dir, "entity_catalog_manual.yml")
	if err := os.WriteFile(manualPath, []byte(clusteringTestManualEntitiesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(manual entities) error = %v", err)
	}
	generatedPath := filepath.Join(dir, "entity_catalog_generated.yml")
	if err := os.WriteFile(generatedPath, []byte(clusteringTestGeneratedEntitiesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(generated entities) error = %v", err)
	}

	// testsupport.DedupTestSettingsYAML otherwise points these three at
	// module-relative paths, which "clustering evaluate" would need to
	// resolve relative to the test process's working directory; replace
	// them with this fixture's own absolute paths.
	settings := strings.Replace(testsupport.DedupTestSettingsYAML, "rules_path: configs/event_clustering_rules.yml", "rules_path: "+rulesPath, 1)
	settings = strings.Replace(settings, "manual_path: configs/entity_catalog_manual.yml", "manual_path: "+manualPath, 1)
	settings = strings.Replace(settings, "generated_path: configs/entity_catalog_generated.yml", "generated_path: "+generatedPath, 1)
	if err := os.WriteFile(configPath, []byte(settings), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	return configPath
}

func writeClusteringFixtures(t *testing.T) (configPath, datasetPath string) {
	t.Helper()
	dir := t.TempDir()

	rulesPath := filepath.Join(dir, "clustering_rules.yml")
	if err := os.WriteFile(rulesPath, []byte(clusteringTestRulesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(rules) error = %v", err)
	}

	configPath = writeClusteringConfigFixture(t, dir, rulesPath)

	datasetPath = filepath.Join(dir, "clustering_groups.yml")
	if err := os.WriteFile(datasetPath, []byte(clusteringTestDatasetYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(dataset) error = %v", err)
	}
	return configPath, datasetPath
}

func TestClusteringEvaluateCmd_PrintsMetrics(t *testing.T) {
	configPath, datasetPath := writeClusteringFixtures(t)

	out, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", datasetPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(out, "pairwise: precision=1.000 recall=1.000 f1=1.000") {
		t.Errorf("output = %q, want a perfect pairwise line (a1/a2 should join one cluster)", out)
	}
	if !strings.Contains(out, "representative_accuracy: 1.000 (1/1 correct)") {
		t.Errorf("output = %q, want representative_accuracy 1/1", out)
	}
	if !strings.Contains(out, "assignments (gold group x predicted cluster):") {
		t.Errorf("output = %q, want an assignments section", out)
	}
}

func TestClusteringEvaluateCmd_MissingDatasetFileReturnsError(t *testing.T) {
	configPath, _ := writeClusteringFixtures(t)

	_, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing dataset file")
	}
}

func TestClusteringEvaluateCmd_InvalidRulesPathReturnsError(t *testing.T) {
	dir := t.TempDir()
	configPath := writeClusteringConfigFixture(t, dir, filepath.Join(dir, "missing-rules.yml"))
	datasetPath := filepath.Join(dir, "clustering_groups.yml")
	if err := os.WriteFile(datasetPath, []byte(clusteringTestDatasetYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(dataset) error = %v", err)
	}

	_, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", datasetPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing rules file")
	}
}

func TestClusteringRulesValidateCmd_Success(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "clustering_rules.yml")
	if err := os.WriteFile(rulesPath, []byte(clusteringTestRulesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(rules) error = %v", err)
	}

	out, err := testsupport.Execute(t, "clustering", "rules", "validate", rulesPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "clustering rules OK: "+rulesPath) {
		t.Errorf("output = %q, want it to contain %q", out, "clustering rules OK: "+rulesPath)
	}
}

func TestClusteringRulesValidateCmd_InvalidFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "clustering_rules.yml")
	invalid := strings.Replace(clusteringTestRulesYAML, `version: "test-1.0.0"`, `version: ""`, 1)
	if err := os.WriteFile(rulesPath, []byte(invalid), 0o600); err != nil {
		t.Fatalf("WriteFile(rules) error = %v", err)
	}

	_, err := testsupport.Execute(t, "clustering", "rules", "validate", rulesPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want a validation error for an empty meta.version")
	}
}

func TestClusteringRulesValidateCmd_MissingFileReturnsError(t *testing.T) {
	_, err := testsupport.Execute(t, "clustering", "rules", "validate", filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing rules file")
	}
}

func TestClusteringEvaluateCmd_GeneratedRulesFlag(t *testing.T) {
	configPath, datasetPath := writeClusteringFixtures(t)
	generatedPath := filepath.Join(t.TempDir(), "event_rules_generated.yml")
	generated := "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: \"inks a deal\", proposal_id: \"evr_1\", prompt_version: \"event-rules-v1\"}\n"
	if err := os.WriteFile(generatedPath, []byte(generated), 0o600); err != nil {
		t.Fatalf("WriteFile(generated rules) error = %v", err)
	}

	base, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", datasetPath)
	if err != nil {
		t.Fatalf("Execute() baseline error = %v", err)
	}
	withGen, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", datasetPath, "--generated-rules", generatedPath)
	if err != nil {
		t.Fatalf("Execute() with --generated-rules error = %v", err)
	}
	articlesLine := func(s string) string {
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "articles:") {
				return strings.TrimSpace(l)
			}
		}
		return ""
	}
	if articlesLine(base) == "" || articlesLine(base) != articlesLine(withGen) {
		t.Errorf("articles line differs: base=%q with=%q", articlesLine(base), articlesLine(withGen))
	}

	// The flag must actually be read: a generated file with an unknown
	// action fails the command only when it is passed.
	badPath := filepath.Join(t.TempDir(), "bad_generated.yml")
	bad := "meta: {version: 1}\naction_terms:\n  teleport:\n    - {phrase: \"zzqx\", proposal_id: \"evr_2\", prompt_version: \"event-rules-v1\"}\n"
	if err := os.WriteFile(badPath, []byte(bad), 0o600); err != nil {
		t.Fatalf("WriteFile(bad generated rules) error = %v", err)
	}
	if _, err := testsupport.Execute(t, "clustering", "evaluate", "--config", configPath, "--dataset", datasetPath, "--generated-rules", badPath); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Errorf("Execute() with a malformed --generated-rules error = %v, want the unknown action reported", err)
	}
}
