// cli/cmd/entities/list_test.go
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

func TestListCommand_PrintsPendingProposalsSortedByScore(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	_ = dbPath
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()

	low := entityproposal.Proposal{
		ID: "low", CanonicalName: "Low Score Player", NormalizedName: "low score player",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Confidence: 0.1, Occurrences: 1, Status: entityproposal.StatusPending,
		FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	high := entityproposal.Proposal{
		ID: "high", CanonicalName: "High Score Player", NormalizedName: "high score player",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Confidence: 0.9, Occurrences: 10, Status: entityproposal.StatusPending,
		FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	if _, err := repo.Upsert(context.Background(), low); err != nil {
		t.Fatalf("Upsert(low) error = %v", err)
	}
	if _, err := repo.Upsert(context.Background(), high); err != nil {
		t.Fatalf("Upsert(high) error = %v", err)
	}

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newListCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list command Execute() error = %v", err)
	}

	output := out.String()
	highIdx := strings.Index(output, "High Score Player")
	lowIdx := strings.Index(output, "Low Score Player")
	if highIdx == -1 || lowIdx == -1 {
		t.Fatalf("output missing an expected row:\n%s", output)
	}
	if highIdx > lowIdx {
		t.Fatalf("High Score Player should be listed before Low Score Player (higher score); got:\n%s", output)
	}
}

func TestListCommand_FiltersByTypeAndStatus(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	coach := entityproposal.Proposal{
		ID: "c1", CanonicalName: "Some Coach", NormalizedName: "some coach",
		EntityType: entityproposal.EntityTypeCoach, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	if _, err := repo.Upsert(context.Background(), coach); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newListCommand()
	cmd.SetArgs([]string{"--type", "player"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list command Execute() error = %v", err)
	}
	if strings.Contains(out.String(), "Some Coach") {
		t.Fatalf("output should not contain a coach when --type=player is set:\n%s", out.String())
	}
}
