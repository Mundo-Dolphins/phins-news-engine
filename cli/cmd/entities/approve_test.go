// cli/cmd/entities/approve_test.go
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
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

// negative_threshold_high is 0 (not the >0 value dictionary.Validate
// rejects — see domain/dictionary/validate.go's
// validateDecision), matching the fixture entitydiscovery's own
// apply_test.go uses for the same reason.
const approveTestDictionaryYAML = `
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
    entries: []
`

func writeApproveTestCatalogFiles(t *testing.T) (dictPath, manualPath string) {
	t.Helper()
	dir := t.TempDir()
	dictPath = filepath.Join(dir, "dictionary.yml")
	manualPath = filepath.Join(dir, "entity_catalog_manual.yml")
	if err := os.WriteFile(dictPath, []byte(approveTestDictionaryYAML), 0o600); err != nil {
		t.Fatalf("write dictionary fixture: %v", err)
	}
	if err := os.WriteFile(manualPath, []byte(approveTestManualYAML), 0o600); err != nil {
		t.Fatalf("write manual fixture: %v", err)
	}
	return dictPath, manualPath
}

const approveTestManualYAML = `entities:
  - id: springfield-isotopes
    canonical_name: Springfield Isotopes
    kind: team
    terms: ["Springfield Isotopes"]
    is_primary_team: true
`

func TestApproveCommand_NewPlayerWritesToDictionaryAndMarksApproved(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", NormalizedName: "jairene mossbank",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"position": "CB", "affiliation_status": "team", "team_id": "springfield-isotopes"}, Confidence: 0.9, Occurrences: 3,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newApproveCommand()
	cmd.SetArgs([]string{id, "--dictionary", dictPath, "--manual", manualPath})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("approve command Execute() error = %v\noutput:\n%s", err, out.String())
	}

	d, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("dictionaryfile.Load() error = %v", err)
	}
	found := false
	for _, cat := range d.Categories {
		for _, e := range cat.Entries {
			if e.CanonicalName == "Jai'Rene' Mossbank" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("Jai'Rene' Mossbank not found in the dictionary after approve")
	}

	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != entityproposal.StatusApproved {
		t.Fatalf("Status = %q, want approved", got.Status)
	}
	if got.DecidedAt.IsZero() {
		t.Fatal("DecidedAt is zero, want it set")
	}
}

func TestApproveCommand_PossibleDuplicateWithoutFlagsErrors(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "John Smith", NormalizedName: "john smith",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindPossibleDuplicate,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newApproveCommand()
	cmd.SetArgs([]string{id, "--dictionary", dictPath, "--manual", manualPath})
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("approve command Execute() error = nil, want an error demanding --merge-into or --as-new")
	}
}

func TestApproveCommand_AlreadyDecidedProposalErrors(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Already Decided", NormalizedName: "already decided",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), id, entityproposal.StatusRejected, now); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newApproveCommand()
	cmd.SetArgs([]string{id, "--dictionary", dictPath, "--manual", manualPath})
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("approve command Execute() error = nil, want an error — proposal is already rejected")
	}
}

// --generated is consulted so a league person already in the generated
// catalog is refused and stays pending.
func TestApproveCommand_GeneratedCatalogDuplicateStaysPending(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		ID: "p1", CanonicalName: "Pierce Quenby", NormalizedName: "pierce quenby",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"team_id": "capital-city-capitals"},
		Status:   entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	manual := "entities:\n  - id: springfield-isotopes\n    canonical_name: Springfield Isotopes\n    kind: team\n    terms: [Springfield Isotopes]\n    is_primary_team: true\n" +
		"  - id: capital-city-capitals\n    canonical_name: Capital City Capitals\n    kind: team\n    terms: [Capital City Capitals]\n"
	if err := os.WriteFile(manualPath, []byte(manual), 0o600); err != nil {
		t.Fatal(err)
	}
	generatedPath := filepath.Join(filepath.Dir(manualPath), "entity_catalog_generated.yml")
	if err := os.WriteFile(generatedPath, []byte("entities:\n  - id: pierce-quenby\n    canonical_name: Pierce Quenby\n    kind: player\n    terms: [Pierce Quenby]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newApproveCommand()
	cmd.SetArgs([]string{id, "--dictionary", dictPath, "--manual", manualPath, "--generated", generatedPath})
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want duplicate error")
	}
	after, _ := os.ReadFile(manualPath)
	if string(after) != manual {
		t.Fatal("manual catalog changed despite error")
	}
	got, _ := repo.Get(context.Background(), id)
	if got.Status != entityproposal.StatusPending {
		t.Fatalf("Status = %s, want pending", got.Status)
	}
}
