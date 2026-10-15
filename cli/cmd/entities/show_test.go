// cli/cmd/entities/show_test.go
package entities

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestShowCommand_PrintsProposalDetail(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()

	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", NormalizedName: "jairene mossbank",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"position": "CB"}, Confidence: 0.9, Occurrences: 3,
		Aliases: []string{"Mossbank"},
		Sources: []entityproposal.Source{
			{ArticleID: "a1", ArticleTitle: "Isotopes claim CB Jai'Rene' Mossbank"},
			{ArticleID: "a2", ArticleTitle: "Springfield adds a cornerback"},
		},
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newShowCommand()
	cmd.SetArgs([]string{id})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("show command Execute() error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"Jai'Rene' Mossbank", "Mossbank", "CB", "Isotopes claim CB Jai'Rene' Mossbank"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestShowCommand_UnknownIDErrors(t *testing.T) {
	dbPath, _ := newDiscoverTestDB(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newShowCommand()
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("show command Execute() error = nil, want an error for an unknown id")
	}
}

func TestShowCommand_PrintsVerificationLines(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata:   map[string]string{"position": "QB", "team": "Capital City Capitals", "league": "NFL"},
		WebSources: []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/x", Title: "x"}},
		Status:     entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	shared.ConfigPath = writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newShowCommand()
	cmd.SetArgs([]string{id})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"Team: Capital City Capitals", "League: NFL", "Web evidence: league.example.com"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestShowCommand_PrintsDecisionReason(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	p := entityproposal.Proposal{
		CanonicalName: "Springfield Mackerels", NormalizedName: "springfield mackerels",
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Status, p.DecisionReason = id, entityproposal.StatusRejected, entityproposal.ReasonAutoRejectedNonNFL
	if err := repo.UpdateVerification(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	shared.ConfigPath = writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newShowCommand()
	cmd.SetArgs([]string{id})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if want := "Decision reason: auto_rejected_non_nfl"; !strings.Contains(out.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, out.String())
	}
}

func TestPrintVerification_ShowsLeagueSuggestions(t *testing.T) {
	var out bytes.Buffer
	printVerification(&out, entityproposal.Proposal{Metadata: map[string]string{
		"suggested_entity": "Dorian Burns", "suggested_team": "WAV", "suggested_basis": "unique_surname",
	}}, true)
	if !strings.Contains(out.String(), "Suggested match: Dorian Burns (WAV), basis unique_surname") {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	printVerification(&out, entityproposal.Proposal{Metadata: map[string]string{
		"suggested_candidates": "Zorin Brackwater (SPR); Osric Brackwater (HC)", "suggested_basis": "surname_shared",
	}}, true)
	if !strings.Contains(out.String(), "Candidates: Zorin Brackwater (SPR); Osric Brackwater (HC) (basis surname_shared)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestShowCommand_PrintsSuggestionsEvenWithoutOtherVerificationData(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		ID: "p2", CanonicalName: "Burns", NormalizedName: "burns", EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"suggested_entity": "Dorian Burns", "suggested_team": "WAV", "suggested_basis": "unique_surname"},
		Status:   entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	shared.ConfigPath = writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newShowCommand()
	cmd.SetArgs([]string{id})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Suggested match: Dorian Burns (WAV)") {
		t.Fatalf("show output lacks the suggestion:\n%s", out.String())
	}
}
