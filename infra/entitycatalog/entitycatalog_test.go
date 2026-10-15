package entitycatalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoad_MergesManualAndGenerated(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - id: springfield-isotopes
    canonical_name: Springfield Isotopes
    kind: team
    is_primary_team: true
    terms: ["isotopes", "springfield isotopes"]
`)
	generated := writeFile(t, dir, "generated.yml", `
entities:
  - id: pierce-quenby
    canonical_name: Pierce Quenby
    kind: player
    position: OT
    terms: ["pierce quenby"]
`)

	entities, err := entitycatalog.Load(manual, generated)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("len(entities) = %d, want 2", len(entities))
	}
	byID := map[string]eventsignals.EntityRule{}
	for _, e := range entities {
		byID[e.ID] = e
	}
	if byID["pierce-quenby"].Kind != eventsignals.EntityKindPlayer || byID["pierce-quenby"].Position != "OT" {
		t.Errorf("pierce-quenby = %+v, missing expected kind/position", byID["pierce-quenby"])
	}
	if !byID["springfield-isotopes"].IsPrimaryTeam {
		t.Errorf("springfield-isotopes.IsPrimaryTeam = false, want true")
	}
}

func TestLoad_RejectsDuplicateIDAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: dup, canonical_name: A, kind: team, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", `
entities:
  - {id: dup, canonical_name: B, kind: player, terms: ["b"]}
`)
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for duplicate id, got nil")
	}
}

func TestLoad_RejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {canonical_name: A, kind: team, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for missing id, got nil")
	}
}

func TestLoad_RejectsUnrecognizedKind(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: a, canonical_name: A, kind: mascot, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for unrecognized kind, got nil")
	}
}

func TestLoad_RejectsEmptyCanonicalName(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: a, canonical_name: "", kind: team, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for empty canonical name, got nil")
	}
}

func TestLoad_RejectsDuplicateCanonicalNameAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: a, canonical_name: "Same Name", kind: team, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", `
entities:
  - {id: b, canonical_name: "Same Name", kind: player, terms: ["b"]}
`)
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for duplicate canonical name, got nil")
	}
}

func TestLoad_RejectsIsPrimaryTeamOnNonTeamKind(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: a, canonical_name: A, kind: player, is_primary_team: true, terms: ["a"]}
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for is_primary_team on a non-team kind, got nil")
	}
}

func TestLoad_RejectsMoreThanOnePrimaryTeam(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", `
entities:
  - {id: a, canonical_name: A, kind: team, is_primary_team: true, terms: ["a"]}
  - {id: b, canonical_name: B, kind: team, is_primary_team: true, terms: ["b"]}
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err == nil {
		t.Fatal("Load: expected error for more than one primary team, got nil")
	}
}

func TestWriteGenerated_RoundTripsThroughLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.yml")
	entries := []eventsignals.EntityRule{
		{ID: "mike-thistlewood", CanonicalName: "Mike Thistlewood", Kind: eventsignals.EntityKindCoach, Terms: []string{"mike thistlewood", "thistlewood"}},
	}
	if err := entitycatalog.WriteGenerated(path, entries); err != nil {
		t.Fatalf("WriteGenerated: %v", err)
	}

	manual := writeFile(t, dir, "manual.yml", "entities: []\n")
	got, err := entitycatalog.Load(manual, path)
	if err != nil {
		t.Fatalf("Load after WriteGenerated: %v", err)
	}
	if len(got) != 1 || got[0].ID != "mike-thistlewood" || got[0].Kind != eventsignals.EntityKindCoach {
		t.Errorf("got = %+v, want the written entry round-tripped", got)
	}
}

func TestLoadOne_ReadsEntitiesWithoutMerging(t *testing.T) {
	entries, err := entitycatalog.LoadOne("testdata/mini_manual.yml")
	if err != nil {
		t.Fatalf("LoadOne() error = %v", err)
	}
	if len(entries) != 1 || entries[0].CanonicalName != "Springfield Isotopes" {
		t.Fatalf("LoadOne() = %+v, want one entry named Springfield Isotopes", entries)
	}
}

func TestWriteManual_RoundTripsThroughLoadOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manual.yml")
	entries := []eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Isotopes", "Isotopes"}, IsPrimaryTeam: true},
	}
	if err := entitycatalog.WriteManual(path, entries); err != nil {
		t.Fatalf("WriteManual() error = %v", err)
	}
	got, err := entitycatalog.LoadOne(path)
	if err != nil {
		t.Fatalf("LoadOne() after WriteManual() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "springfield-isotopes" || !got[0].IsPrimaryTeam {
		t.Fatalf("LoadOne() after WriteManual() = %+v, want the written entry back unchanged", got)
	}
}

const teamAndPlayerManual = `
entities:
  - id: capital-city-capitals
    canonical_name: Capital City Capitals
    kind: team
    terms: ["capitals"]
`

func TestLoad_TeamIDAndRole(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", teamAndPlayerManual+`
  - id: patrick-ostrander
    canonical_name: Patrick Ostrander
    kind: player
    position: QB
    team_id: capital-city-capitals
    role: starter
    terms: ["ostrander"]
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	entities, err := entitycatalog.Load(manual, generated)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got eventsignals.EntityRule
	for _, e := range entities {
		if e.ID == "patrick-ostrander" {
			got = e
		}
	}
	if got.TeamID != "capital-city-capitals" || got.Role != "starter" || got.Position != "QB" {
		t.Errorf("got %+v", got)
	}
}

func TestLoad_RejectsUnknownTeamID(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", teamAndPlayerManual+`
  - id: patrick-ostrander
    canonical_name: Patrick Ostrander
    kind: player
    team_id: nope
    terms: ["ostrander"]
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	_, err := entitycatalog.Load(manual, generated)
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("err = %v, want team_id error", err)
	}
}

func TestLoad_RejectsTeamIDPointingAtNonTeam(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", teamAndPlayerManual+`
  - id: tobin-kestrell
    canonical_name: Tobin Kestrell
    kind: player
    terms: ["kestrell"]
  - id: patrick-ostrander
    canonical_name: Patrick Ostrander
    kind: player
    team_id: tobin-kestrell
    terms: ["ostrander"]
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	_, err := entitycatalog.Load(manual, generated)
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("err = %v, want team_id error", err)
	}
}

func TestLoad_EmptyTeamIDAllowed(t *testing.T) {
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", teamAndPlayerManual+`
  - id: patrick-ostrander
    canonical_name: Patrick Ostrander
    kind: player
    terms: ["ostrander"]
`)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	if _, err := entitycatalog.Load(manual, generated); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestWriteManual_RoundTripsTeamIDAndRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.yml")
	in := []eventsignals.EntityRule{{
		ID: "patrick-ostrander", CanonicalName: "Patrick Ostrander", Kind: eventsignals.EntityKindPlayer,
		Terms: []string{"ostrander"}, Position: "QB", TeamID: "capital-city-capitals", Role: "starter",
	}}
	if err := entitycatalog.WriteManual(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := entitycatalog.LoadOne(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].TeamID != "capital-city-capitals" || out[0].Role != "starter" {
		t.Errorf("out = %+v", out)
	}
}

func TestLoad_ExampleConfigs(t *testing.T) {
	if _, err := entitycatalog.Load(repofs.ExampleConfig(t, "entity_catalog_manual.yml"), repofs.ExampleConfig(t, "entity_catalog_generated.yml")); err != nil {
		t.Fatalf("example configs: %v", err)
	}
}

const affiliationManualHeader = `
entities:
  - id: capital-city-capitals
    canonical_name: Capital City Capitals
    kind: team
    terms: ["capitals"]
`

func loadAffiliation(t *testing.T, body string) ([]eventsignals.EntityRule, error) {
	t.Helper()
	dir := t.TempDir()
	manual := writeFile(t, dir, "manual.yml", affiliationManualHeader+body)
	generated := writeFile(t, dir, "generated.yml", "entities: []\n")
	return entitycatalog.Load(manual, generated)
}

func TestLoad_AffiliationStatusTeamValid(t *testing.T) {
	rules, err := loadAffiliation(t, `
  - id: patrick-ostrander
    canonical_name: Patrick Ostrander
    kind: player
    position: QB
    affiliation_status: team
    team_id: capital-city-capitals
    terms: ["ostrander"]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := rules[1].AffiliationStatus; got != eventsignals.AffiliationTeam {
		t.Errorf("status = %q", got)
	}
}

func TestLoad_AffiliationStatusInvalid(t *testing.T) {
	player := func(extra string) string {
		return "\n  - id: p\n    canonical_name: P\n    kind: player\n    terms: [\"p\"]\n" + extra
	}
	tests := map[string]string{
		"team without team_id":    player("    affiliation_status: team\n"),
		"team with unknown team":  player("    affiliation_status: team\n    team_id: nowhere\n"),
		"free agent with team_id": player("    affiliation_status: free_agent\n    team_id: capital-city-capitals\n"),
		"unknown with team_id":    player("    affiliation_status: unknown\n    team_id: capital-city-capitals\n"),
		"bogus status":            player("    affiliation_status: retired\n"),
		"free agent on team":      "\n  - id: t2\n    canonical_name: T2\n    kind: team\n    terms: [\"t2\"]\n    affiliation_status: free_agent\n",
		"free agent on coach":     "\n  - id: c\n    canonical_name: C\n    kind: coach\n    terms: [\"c\"]\n    affiliation_status: free_agent\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadAffiliation(t, body); err == nil {
				t.Fatal("Load error = nil, want error")
			}
		})
	}
}

func TestLoad_FreeAgentAndUnknownValid(t *testing.T) {
	rules, err := loadAffiliation(t, `
  - id: example-player
    canonical_name: Example Player
    kind: player
    position: WR
    affiliation_status: free_agent
    terms: ["example player"]
  - id: other-player
    canonical_name: Other Player
    kind: player
    affiliation_status: unknown
    terms: ["other player"]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rules[1].AffiliationStatus != eventsignals.AffiliationFreeAgent || rules[2].AffiliationStatus != eventsignals.AffiliationUnknown {
		t.Errorf("statuses = %q, %q", rules[1].AffiliationStatus, rules[2].AffiliationStatus)
	}
}

func TestLoad_LegacyEntriesStayBackwardCompatible(t *testing.T) {
	rules, err := loadAffiliation(t, `
  - id: legacy-team-player
    canonical_name: Legacy Team Player
    kind: player
    team_id: capital-city-capitals
    terms: ["ltp"]
  - id: legacy-bare
    canonical_name: Legacy Bare
    kind: player
    terms: ["lb"]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := rules[1].EffectiveAffiliation(); got != eventsignals.AffiliationTeam {
		t.Errorf("legacy team_id => %q", got)
	}
	if got := rules[2].EffectiveAffiliation(); got != eventsignals.AffiliationUnspecified {
		t.Errorf("legacy empty team_id => %q, must not become free_agent/unknown", got)
	}
}

func TestWriteManual_RoundTripsAffiliationStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.yml")
	in := []eventsignals.EntityRule{
		{ID: "fa", CanonicalName: "FA", Kind: eventsignals.EntityKindPlayer, Terms: []string{"fa"}, AffiliationStatus: eventsignals.AffiliationFreeAgent},
		{ID: "legacy", CanonicalName: "Legacy", Kind: eventsignals.EntityKindPlayer, Terms: []string{"l"}},
	}
	if err := entitycatalog.WriteManual(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := entitycatalog.LoadOne(path)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].AffiliationStatus != eventsignals.AffiliationFreeAgent || out[1].AffiliationStatus != "" {
		t.Errorf("round trip = %+v", out)
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "affiliation_status") != 1 {
		t.Errorf("legacy entry must not emit affiliation_status:\n%s", raw)
	}
}
