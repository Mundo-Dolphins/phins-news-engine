// cli/cmd/entities/reject_test.go
package entities

import (
	"bytes"
	"context"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestRejectCommand_MarksProposalRejectedWithoutTouchingCatalogFiles(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	now := time.Now().UTC()
	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Some Candidate", NormalizedName: "some candidate",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(context.Background(), p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://unused.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newRejectCommand()
	cmd.SetArgs([]string{id})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reject command Execute() error = %v", err)
	}

	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != entityproposal.StatusRejected {
		t.Fatalf("Status = %q, want rejected", got.Status)
	}
}
