package backtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestBacktestLeague_RosterIsRequired(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	_, err := testsupport.Execute(t, "backtest", "league", "--config", cfgPath, "--db", dbPath, "--roster", filepath.Join(t.TempDir(), "missing.csv"))
	if err == nil {
		t.Fatal("a missing roster must be an error, not an empty OK report")
	}
}

func TestBacktestLeague_EndToEndAgainstFixtureData(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	root := repofs.Path(t, "configs", "example")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	block := "clustering:\n  enabled: true\n  event_threshold: 0.55\n  rules_path: " + filepath.Join(root, "event_clustering_rules.yml") +
		"\n  entity_catalog:\n    manual_path: " + filepath.Join(root, "entity_catalog_manual.yml") +
		"\n    generated_path: " + filepath.Join(root, "entity_catalog_generated.yml") + "\n  candidate_limit: 50\n"
	patched := strings.Replace(string(raw), "clustering:\n  enabled: false\n", block, 1)
	if patched == string(raw) {
		t.Fatal("fixture config no longer has the clustering block this test patches")
	}
	if err := os.WriteFile(cfgPath, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(roster, []byte("full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\nAvery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")

	out, err := testsupport.Execute(t, "backtest", "league", "--config", cfgPath, "--db", dbPath, "--days", "14",
		"--roster", roster, "--league-rules", filepath.Join(root, "league_alias_rules.yml"))
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "invariant: OK") || !strings.Contains(out, "articles: ") {
		t.Fatalf("output =\n%s", out)
	}
}

func TestBacktestLeague_LoadsTheConfiguredGeneratedRules(t *testing.T) {
	cfgPath := testsupport.WriteFixture(t)
	root := repofs.Path(t, "configs", "example")
	// A malformed generated file proves the command reads it, as main.go does.
	generated := filepath.Join(t.TempDir(), "event_rules_generated.yml")
	if err := os.WriteFile(generated, []byte("meta: {version: 1}\naction_terms:\n  teleport:\n    - {phrase: zzqx, proposal_id: a, prompt_version: v}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	block := "clustering:\n  enabled: true\n  event_threshold: 0.55\n  rules_path: " + filepath.Join(root, "event_clustering_rules.yml") +
		"\n  generated_rules_path: " + generated +
		"\n  entity_catalog:\n    manual_path: " + filepath.Join(root, "entity_catalog_manual.yml") +
		"\n    generated_path: " + filepath.Join(root, "entity_catalog_generated.yml") + "\n  candidate_limit: 50\n"
	patched := strings.Replace(string(raw), "clustering:\n  enabled: false\n", block, 1)
	if err := os.WriteFile(cfgPath, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(roster, []byte("full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\nAvery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(filepath.Dir(cfgPath), "operational.db")
	_, err = testsupport.Execute(t, "backtest", "league", "--config", cfgPath, "--db", dbPath, "--days", "14",
		"--roster", roster, "--league-rules", filepath.Join(root, "league_alias_rules.yml"))
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("err = %v; a malformed generated rules file must fail the backtest like the service startup", err)
	}
}
