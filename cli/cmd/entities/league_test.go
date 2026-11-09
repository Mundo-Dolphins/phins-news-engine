package entities

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// leagueEvalFixture writes settings with entity_discovery.league enabled
// (when leagueOn), a one-team rules file, a matching manual catalog and a
// small roster, and seeds pending player proposals. It returns the settings
// path and the catalog paths.
func leagueEvalFixture(t *testing.T, leagueOn bool) (settingsPath, manualPath, generatedPath string) {
	t.Helper()
	dbPath, db := newDiscoverTestDB(t)
	settingsPath = writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	dir := filepath.Dir(settingsPath)
	manualPath = filepath.Join(dir, "manual.yml")
	generatedPath = filepath.Join(dir, "generated.yml")
	rulesPath := filepath.Join(dir, "league_rules.yml")
	rosterPath := filepath.Join(dir, "roster.csv")

	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write(manualPath, "entities:\n  - id: springfield-isotopes\n    canonical_name: Springfield Isotopes\n    kind: team\n    terms: [isotopes, springfield isotopes]\n    is_primary_team: true\n    position: \"\"\n")
	write(rulesPath, "noise_min_letters: 3\ncommon_words: [all, good]\nundecided_terms: [league]\nteams:\n  - {abbr: SPR, name: Springfield Isotopes, aliases: [isotopes], upper_abbrs: [SPR]}\ncorrections: []\n")
	write(rosterPath, "full_name,team,position,status,gsis_id,week\nZeno Ashbury,SPR,LB,ACT,g1,5\nOrrin Pellwick,SPR,QB,ACT,g2,5\n")

	enabled := "false"
	if leagueOn {
		enabled = "true"
	}
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	extra := "  league:\n    enabled: " + enabled + "\n    rules_path: " + rulesPath + "\n  roster:\n    enabled: true\n    path: " + rosterPath + "\n    max_age_days: 0\n"
	write(settingsPath, string(b)+extra)

	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i, p := range []struct{ name, alias string }{
		{"Zeno Ashbury", "Ashbury"},
		{"Orrin Pellwick", "Pellwik"},
		{"Quill Farrowby", "Farrowby"},
	} {
		if _, err := repo.Upsert(context.Background(), entityproposal.Proposal{
			CanonicalName: p.name, NormalizedName: entityproposal.Normalize(p.name),
			EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Metadata: map[string]string{"league": "NFL"}, Confidence: 0.9, Occurrences: 1 + i,
			Aliases: []string{p.alias},
			Status:  entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
		}); err != nil {
			t.Fatalf("Upsert(%s) error = %v", p.name, err)
		}
	}
	return settingsPath, manualPath, generatedPath
}

func runLeagueEvaluate(t *testing.T, settingsPath, manualPath, generatedPath string) (stdout, stderr string, err error) {
	t.Helper()
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()
	cmd := newLeagueEvaluateCommand()
	cmd.SetArgs([]string{"--manual", manualPath, "--generated", generatedPath})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestLeagueEvaluateGolden(t *testing.T) {
	settingsPath, manualPath, generatedPath := leagueEvalFixture(t, true)
	stdout, stderr, err := runLeagueEvaluate(t, settingsPath, manualPath, generatedPath)
	if err != nil {
		t.Fatalf("league evaluate error = %v\nstderr:\n%s", err, stderr)
	}
	want := "ground truth: 3 single-word aliases of verified NFL players\n" +
		"  not_found: 1/2 correct\n" +
		"  suggested: 1/1 correct\n" +
		"outside the roster: 0/1 wrongly resolved (target 0; check each listed name for a nickname variant of the same person)\n"
	if stdout != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestLeagueEvaluateDisabled(t *testing.T) {
	settingsPath, manualPath, generatedPath := leagueEvalFixture(t, false)
	stdout, _, err := runLeagueEvaluate(t, settingsPath, manualPath, generatedPath)
	if err == nil || err.Error() != "entities league evaluate: entity_discovery.league is disabled" {
		t.Fatalf("error = %v, want the disabled error", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}
