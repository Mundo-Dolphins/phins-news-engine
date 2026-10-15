package entities

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

const (
	extractRulesPath  = "event_clustering_rules.yml"
	extractLeagueRule = "league_alias_rules.yml"
)

// extractFixture writes a tiny manual catalog (two teams and a player) and an
// empty generated one, so the test does not depend on the live roster.
func extractFixture(t *testing.T) (manual, generated string) {
	t.Helper()
	dir := t.TempDir()
	manual = filepath.Join(dir, "manual.yml")
	generated = filepath.Join(dir, "generated.yml")
	body := `entities:
    - id: springfield-isotopes
      canonical_name: Springfield Isotopes
      kind: team
      terms: [isotopes, springfield isotopes]
      is_primary_team: true
    - id: shelbyville-sharks
      canonical_name: Shelbyville Sharks
      kind: team
      terms: [sharks, shelbyville sharks]
    - id: jordan-sample
      canonical_name: Jordan Sample
      kind: player
      terms: [Jordan Sample, Sample]
`
	if err := os.WriteFile(manual, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("entities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return manual, generated
}

func runExtract(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newExtractCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), err
}

func baseArgs(t *testing.T) []string {
	manual, generated := extractFixture(t)
	return []string{"--rules", repofs.ExampleConfig(t, extractRulesPath), "--manual", manual, "--generated", generated}
}

func TestExtract_ReportsRecognisedAndUnknownEntities(t *testing.T) {
	args := append(baseArgs(t), "--title", "Isotopes sign Random Newcomer as Jordan Sample watches")
	out, err := runExtract(t, "", args...)
	if err != nil {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	for _, want := range []string{"Primary entity: Jordan Sample", "Springfield Isotopes", "Random Newcomer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Unknown entities") {
		t.Errorf("output must list the unknown entity section:\n%s", out)
	}
}

func TestExtract_BodyFromStdin(t *testing.T) {
	args := append(baseArgs(t), "--title", "Weekend notes", "--body-file", "-")
	out, err := runExtract(t, "Shelbyville Sharks visit the Isotopes on Sunday.", args...)
	if err != nil {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Shelbyville Sharks") || !strings.Contains(out, "body") {
		t.Fatalf("a body-only mention must be found and labelled as body:\n%s", out)
	}
}

func TestExtract_LeagueFlagAddsScopedMentionsOnly(t *testing.T) {
	roster := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(roster, []byte("full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\nAvery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := append(baseArgs(t), "--title", "Avery Placeholder faces the Isotopes")

	without, err := runExtract(t, "", base...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "league") {
		t.Fatalf("without --league no league mention may appear:\n%s", without)
	}

	with, err := runExtract(t, "", append(base, "--league", "--roster", roster, "--league-rules", repofs.ExampleConfig(t, extractLeagueRule))...)
	if err != nil {
		t.Fatalf("extract --league: %v\n%s", err, with)
	}
	if !strings.Contains(with, "Avery Placeholder") || !strings.Contains(with, "league") {
		t.Fatalf("--league must record Avery Placeholder with scope league:\n%s", with)
	}
	if strings.Contains(with, "Primary entity: Avery Placeholder") {
		t.Fatalf("a league player must never be the primary entity:\n%s", with)
	}
}

func TestExtract_JSONFormat(t *testing.T) {
	args := append(baseArgs(t), "--title", "Jordan Sample and the Sharks", "--format", "json")
	out, err := runExtract(t, "", args...)
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep["PrimaryEntity"] != "Jordan Sample" {
		t.Fatalf("PrimaryEntity = %v", rep["PrimaryEntity"])
	}
}

func TestExtract_RequiresSomeText(t *testing.T) {
	if _, err := runExtract(t, "", baseArgs(t)...); err == nil {
		t.Fatal("no title, description or body must be a usage error")
	}
	if _, err := runExtract(t, "", append(baseArgs(t), "--title", "x", "--format", "xml")...); err == nil {
		t.Fatal("an unknown --format must be an error")
	}
}

func TestExtract_GeneratedRulesFlagMergesThePhrases(t *testing.T) {
	gen := filepath.Join(t.TempDir(), "event_rules_generated.yml")
	if err := os.WriteFile(gen, []byte("meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: v1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	action := func(extra ...string) string {
		t.Helper()
		args := append(append(baseArgs(t), "--format", "json", "--title", "Isotopes zzqx pens a pact"), extra...)
		out, err := runExtract(t, "", args...)
		if err != nil {
			t.Fatalf("extract: %v\n%s", err, out)
		}
		var rep struct{ Action string }
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		return rep.Action
	}
	if got := action(); got != "" {
		t.Fatalf("without generated rules Action = %q, want none", got)
	}
	if got := action("--generated-rules", gen); got != "sign" {
		t.Fatalf("with --generated-rules Action = %q, want sign", got)
	}
}
