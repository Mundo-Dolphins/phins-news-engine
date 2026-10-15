package entitydiscovery_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
)

// negative_threshold_high is 0 (not the >0 value dictionary.Validate
// rejects — see domain/dictionary/validate.go's
// validateDecision) so this fixture is itself a validatable dictionary;
// WriteCandidate runs Validate on every apply and must not fail on the
// fixture's own baseline data.
const minimalDictionaryYAML = `
meta:
  version: "1.0.0"
  name: isotopes
  language: en
decision:
  positive_threshold_high: 0.8
  positive_threshold_medium: 0.5
  negative_threshold_high: 0
  title_weight_multiplier: 1.5
  max_count_per_term: 5
categories:
  active_roster_core:
    weight: 1
    entries:
      - canonical_name: "Pierce Quenby"
        type: person
        confidence: 0.9
        updated_at: "2026-01-01"
  coaching_staff:
    weight: 1
    entries:
      - canonical_name: "Jeff Harrowby"
        type: person
        confidence: 0.9
        updated_at: "2026-01-01"
`

const minimalManualYAML = `
entities:
  - id: springfield-isotopes
    canonical_name: "Springfield Isotopes"
    kind: team
    terms: ["Springfield Isotopes", "Isotopes"]
    is_primary_team: true
  - id: capital-city-capitals
    canonical_name: "Capital City Capitals"
    kind: team
    terms: ["Capital City Capitals", "Capitals"]
`

func writeTestCatalogFiles(t *testing.T) entitydiscovery.CatalogPaths {
	t.Helper()
	dir := t.TempDir()
	dictPath := filepath.Join(dir, "dictionary.yml")
	manualPath := filepath.Join(dir, "entity_catalog_manual.yml")
	if err := os.WriteFile(dictPath, []byte(minimalDictionaryYAML), 0o600); err != nil {
		t.Fatalf("write dictionary fixture: %v", err)
	}
	if err := os.WriteFile(manualPath, []byte(minimalManualYAML), 0o600); err != nil {
		t.Fatalf("write manual catalog fixture: %v", err)
	}
	return entitydiscovery.CatalogPaths{DictionaryPath: dictPath, ManualCatalogPath: manualPath}
}

func TestApplyApproval_NewPlayerEntityAppendsToDictionary(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", Kind: entityproposal.KindNewEntity,
		EntityType: entityproposal.EntityTypePlayer, Aliases: []string{"Mossbank"},
		Metadata: map[string]string{"position": "CB", "affiliation_status": "team", "team_id": "springfield-isotopes"},
	}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}

	d, err := dictionaryfile.Load(paths.DictionaryPath)
	if err != nil {
		t.Fatalf("dictionaryfile.Load() error = %v", err)
	}
	found := false
	for _, cat := range d.Categories {
		if cat.Name != "active_roster_core" {
			continue
		}
		for _, e := range cat.Entries {
			if e.CanonicalName == "Jai'Rene' Mossbank" {
				found = true
				if e.Position != "CB" {
					t.Errorf("entry.Position = %q, want CB", e.Position)
				}
			}
		}
	}
	if !found {
		t.Fatal("Jai'Rene' Mossbank not found in active_roster_core after ApplyApproval()")
	}
	if d.Version == "1.0.0" {
		t.Error("dictionary version was not bumped by ApplyApproval()")
	}
}

func TestApplyApproval_RejectsDuplicateCanonicalName(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{ID: "p1", CanonicalName: "Pierce Quenby", Kind: entityproposal.KindNewEntity, EntityType: entityproposal.EntityTypePlayer, Metadata: map[string]string{"team_id": "springfield-isotopes"}}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("ApplyApproval() error = nil, want an error — Pierce Quenby already exists")
	}
}

func TestApplyApproval_NewTeamEntityAppendsToManualCatalog(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Shelbyville Sharks", Kind: entityproposal.KindNewEntity,
		EntityType: entityproposal.EntityTypeTeam, Aliases: []string{"Sharks"},
	}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}

	entries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		t.Fatalf("LoadOne() error = %v", err)
	}
	found := false
	for _, e := range entries {
		if e.CanonicalName == "Shelbyville Sharks" {
			found = true
		}
	}
	if !found {
		t.Fatal("Shelbyville Sharks not found in manual catalog after ApplyApproval()")
	}
}

func TestApplyApproval_NewAliasAgainstDictionaryEntityAppendsAlias(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{
		ID: "p1", Kind: entityproposal.KindNewAlias, EntityType: entityproposal.EntityTypeCoach,
		TargetEntityID: "jeff-harrowby", Aliases: []string{"Coach Harrowby"},
	}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}

	d, err := dictionaryfile.Load(paths.DictionaryPath)
	if err != nil {
		t.Fatalf("dictionaryfile.Load() error = %v", err)
	}
	found := false
	for _, cat := range d.Categories {
		for _, e := range cat.Entries {
			if e.CanonicalName == "Jeff Harrowby" {
				for _, a := range e.Aliases {
					if a == "Coach Harrowby" {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("Coach Harrowby was not added as an alias of Jeff Harrowby")
	}
}

func TestApplyApproval_NewAliasAgainstManualCatalogEntityAppendsTerm(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{
		ID: "p1", Kind: entityproposal.KindNewAlias, EntityType: entityproposal.EntityTypeTeam,
		TargetEntityID: "springfield-isotopes", Aliases: []string{"Topes"},
	}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}

	entries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		t.Fatalf("LoadOne() error = %v", err)
	}
	found := false
	for _, e := range entries {
		if e.ID == "springfield-isotopes" {
			for _, term := range e.Terms {
				if term == "Topes" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("Topes was not added as a term of springfield-isotopes")
	}
}

func TestApplyApproval_UnknownTargetEntityIDErrors(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{ID: "p1", Kind: entityproposal.KindNewAlias, TargetEntityID: "does-not-exist", Aliases: []string{"X"}}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("ApplyApproval() error = nil, want an error for an unknown target_entity_id")
	}
}

func TestApplyApproval_PossibleDuplicateKindIsRejected(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := entityproposal.Proposal{ID: "p1", Kind: entityproposal.KindPossibleDuplicate, CanonicalName: "X", EntityType: entityproposal.EntityTypePlayer}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("ApplyApproval() error = nil, want an error — the CLI must resolve possible_duplicate to new_entity/new_alias first")
	}
}

func leaguePerson(name string, typ entityproposal.EntityType, meta map[string]string) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: "p1", CanonicalName: name, Kind: entityproposal.KindNewEntity,
		EntityType: typ, Aliases: []string{"Alias " + name}, Metadata: meta,
	}
}

func TestApplyApproval_LeaguePlayerGoesToManualCatalog(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	before, _ := os.ReadFile(paths.DictionaryPath)
	p := leaguePerson("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"team_id": "capital-city-capitals", "position": "QB"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}
	after, _ := os.ReadFile(paths.DictionaryPath)
	if string(before) != string(after) {
		t.Fatal("dictionary file changed for a non-Isotopes player")
	}
	entries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == "patrick-ostrander" {
			if e.Kind != "player" || e.TeamID != "capital-city-capitals" || e.Position != "QB" || e.IsPrimaryTeam {
				t.Errorf("unexpected entry %+v", e)
			}
			if len(e.Terms) != 2 || e.Terms[0] != "Patrick Ostrander" || e.Terms[1] != "Alias Patrick Ostrander" {
				t.Errorf("Terms = %v", e.Terms)
			}
			return
		}
	}
	t.Fatal("patrick-ostrander not in manual catalog")
}

func TestApplyApproval_LeagueCoachGoesToManualCatalogWithRole(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	before, _ := os.ReadFile(paths.DictionaryPath)
	p := leaguePerson("Brian Fennow", entityproposal.EntityTypeCoach, map[string]string{"team_id": "capital-city-capitals", "role": "defensive coordinator"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}
	after, _ := os.ReadFile(paths.DictionaryPath)
	if string(before) != string(after) {
		t.Fatal("dictionary file changed for a non-Isotopes coach")
	}
	entries, _ := entitycatalog.LoadOne(paths.ManualCatalogPath)
	for _, e := range entries {
		if e.ID == "brian-fennow" {
			if e.Kind != "coach" || e.Role != "defensive coordinator" || e.TeamID != "capital-city-capitals" {
				t.Errorf("unexpected entry %+v", e)
			}
			return
		}
	}
	t.Fatal("brian-fennow not in manual catalog")
}

func TestApplyApproval_LeaguePersonUnknownTeamErrorsWritesNothing(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	beforeM, _ := os.ReadFile(paths.ManualCatalogPath)
	beforeD, _ := os.ReadFile(paths.DictionaryPath)
	p := leaguePerson("Some Guy", entityproposal.EntityTypePlayer, map[string]string{"team_id": "nowhere-fc"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("want error for unknown team_id")
	}
	afterM, _ := os.ReadFile(paths.ManualCatalogPath)
	afterD, _ := os.ReadFile(paths.DictionaryPath)
	if string(beforeM) != string(afterM) || string(beforeD) != string(afterD) {
		t.Fatal("files changed despite error")
	}
}

func TestApplyApproval_LeaguePersonDuplicateErrors(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := leaguePerson("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"team_id": "capital-city-capitals"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("want duplicate id error")
	}
	// same canonical name, different slug source
	p.ID = "p2"
	p.CanonicalName = "patrick  ostrander"
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("want duplicate canonical name error")
	}
}

func TestApplyApproval_PlayerWithIsotopesTeamIDStaysInDictionary(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := leaguePerson("Jai'Rene' Mossbank", entityproposal.EntityTypePlayer, map[string]string{"team_id": "springfield-isotopes", "position": "CB"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	d, _ := dictionaryfile.Load(paths.DictionaryPath)
	for _, cat := range d.Categories {
		if cat.Name == "active_roster_core" {
			for _, e := range cat.Entries {
				if e.CanonicalName == "Jai'Rene' Mossbank" {
					return
				}
			}
		}
	}
	t.Fatal("Isotopes player not in active_roster_core")
}

// A league person whose team could not be resolved to a team_id must not
// fall back to the Isotopes dictionary.
func TestApplyApproval_PersonWithUnresolvedTeamErrorsWritesNothing(t *testing.T) {
	for _, team := range []string{"Capital City Capitals", "Nowhere FC"} {
		paths := writeTestCatalogFiles(t)
		beforeM, _ := os.ReadFile(paths.ManualCatalogPath)
		beforeD, _ := os.ReadFile(paths.DictionaryPath)
		p := leaguePerson("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"team": team, "position": "QB"})
		if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
			t.Fatalf("team %q: want error for a team without team_id", team)
		}
		afterM, _ := os.ReadFile(paths.ManualCatalogPath)
		afterD, _ := os.ReadFile(paths.DictionaryPath)
		if string(beforeM) != string(afterM) || string(beforeD) != string(afterD) {
			t.Fatalf("team %q: files changed despite error", team)
		}
	}
}

func TestApplyApproval_PersonWithPrimaryTeamNameStaysInDictionary(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := leaguePerson("Jai'Rene' Mossbank", entityproposal.EntityTypePlayer, map[string]string{"team": "Springfield Isotopes", "position": "CB"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	d, _ := dictionaryfile.Load(paths.DictionaryPath)
	for _, cat := range d.Categories {
		for _, e := range cat.Entries {
			if e.CanonicalName == "Jai'Rene' Mossbank" {
				return
			}
		}
	}
	t.Fatal("Isotopes player not in dictionary")
}

// The runtime loads manual+generated together, so a league person whose
// id/name already lives in the generated catalog must be refused (finding 6).
func TestApplyApproval_DuplicateInGeneratedCatalogErrorsWritesNothing(t *testing.T) {
	for _, name := range []string{"Pierce Quenby", "PIERCE  quenby"} {
		paths := writeTestCatalogFiles(t)
		paths.GeneratedCatalogPath = filepath.Join(filepath.Dir(paths.ManualCatalogPath), "entity_catalog_generated.yml")
		gen := "entities:\n  - id: pierce-quenby\n    canonical_name: \"Pierce Quenby\"\n    kind: player\n    terms: [\"Pierce Quenby\"]\n"
		if err := os.WriteFile(paths.GeneratedCatalogPath, []byte(gen), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeM, _ := os.ReadFile(paths.ManualCatalogPath)
		p := leaguePerson(name, entityproposal.EntityTypePlayer, map[string]string{"team_id": "capital-city-capitals"})
		if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
			t.Fatalf("%q: want duplicate error against the generated catalog", name)
		}
		afterM, _ := os.ReadFile(paths.ManualCatalogPath)
		if string(beforeM) != string(afterM) {
			t.Fatalf("%q: manual catalog changed despite error", name)
		}
	}
}

func TestApplyApproval_MissingGeneratedCatalogIsIgnored(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	paths.GeneratedCatalogPath = filepath.Join(t.TempDir(), "absent.yml")
	p := leaguePerson("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"team_id": "capital-city-capitals"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}
}

func dictBytes(t *testing.T, paths entitydiscovery.CatalogPaths) (string, string) {
	t.Helper()
	d, _ := os.ReadFile(paths.DictionaryPath)
	m, _ := os.ReadFile(paths.ManualCatalogPath)
	return string(d), string(m)
}

func TestApplyApproval_FreeAgentGoesToManualCatalogNeverDictionary(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	beforeD, _ := dictBytes(t, paths)
	p := leaguePerson("Example Player", entityproposal.EntityTypePlayer, map[string]string{
		"affiliation_status": "free_agent", "position": "WR", "league": "NFL",
	})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}
	afterD, _ := dictBytes(t, paths)
	if beforeD != afterD {
		t.Fatal("dictionary (active_roster_core) changed for a free agent")
	}
	entries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == "example-player" {
			if e.AffiliationStatus != "free_agent" || e.TeamID != "" || e.Position != "WR" || e.Kind != "player" {
				t.Errorf("unexpected entry %+v", e)
			}
			return
		}
	}
	t.Fatal("free agent not in manual catalog")
}

func TestApplyApproval_UnresolvedAffiliationNeverWritesIsotopes(t *testing.T) {
	cases := map[string]map[string]string{
		"explicit unknown":       {"affiliation_status": "unknown", "position": "WR"},
		"no affiliation at all":  {"position": "WR"},
		"nil metadata":           nil,
		"team status no team_id": {"affiliation_status": "team", "position": "WR"},
		"team status bad id":     {"affiliation_status": "team", "team_id": "nowhere-fc"},
		"free agent with team":   {"affiliation_status": "free_agent", "team_id": "capital-city-capitals"},
		"free agent team name":   {"affiliation_status": "free_agent", "team": "Capital City Capitals"},
		"bogus status":           {"affiliation_status": "retired"},
	}
	for name, md := range cases {
		t.Run(name, func(t *testing.T) {
			paths := writeTestCatalogFiles(t)
			beforeD, beforeM := dictBytes(t, paths)
			p := leaguePerson("Some Guy", entityproposal.EntityTypePlayer, md)
			if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
				t.Fatal("want error, got nil")
			}
			afterD, afterM := dictBytes(t, paths)
			if beforeD != afterD || beforeM != afterM {
				t.Fatal("files changed despite error")
			}
		})
	}
}

func TestApplyApproval_ExplicitTeamStatusRouting(t *testing.T) {
	t.Run("isotopes stays in dictionary", func(t *testing.T) {
		paths := writeTestCatalogFiles(t)
		_, beforeM := dictBytes(t, paths)
		p := leaguePerson("Jai'Rene' Mossbank", entityproposal.EntityTypePlayer, map[string]string{"affiliation_status": "team", "team_id": "springfield-isotopes", "position": "CB"})
		if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		d, _ := dictionaryfile.Load(paths.DictionaryPath)
		found := false
		for _, cat := range d.Categories {
			for _, e := range cat.Entries {
				found = found || e.CanonicalName == "Jai'Rene' Mossbank"
			}
		}
		if !found {
			t.Fatal("Isotopes player not in dictionary")
		}
		if _, afterM := dictBytes(t, paths); beforeM != afterM {
			t.Fatal("manual catalog changed")
		}
	})
	t.Run("opposing goes to manual catalog with status", func(t *testing.T) {
		paths := writeTestCatalogFiles(t)
		beforeD, _ := dictBytes(t, paths)
		p := leaguePerson("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"affiliation_status": "team", "team_id": "capital-city-capitals", "position": "QB"})
		if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if afterD, _ := dictBytes(t, paths); beforeD != afterD {
			t.Fatal("dictionary changed")
		}
		entries, _ := entitycatalog.LoadOne(paths.ManualCatalogPath)
		for _, e := range entries {
			if e.ID == "patrick-ostrander" {
				if e.TeamID != "capital-city-capitals" || e.AffiliationStatus != "team" {
					t.Errorf("unexpected entry %+v", e)
				}
				return
			}
		}
		t.Fatal("not in manual catalog")
	})
}

func TestApplyApproval_FreeAgentCoachRefused(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := leaguePerson("Some Coach", entityproposal.EntityTypeCoach, map[string]string{"affiliation_status": "free_agent"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("want error: only players can be free agents")
	}
}
