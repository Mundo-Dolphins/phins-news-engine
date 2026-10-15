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

func TestReviewCommand_ApprovesSelectedProposalWithoutIDInput(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Review Test Player", NormalizedName: "review test player",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata:   map[string]string{"affiliation_status": "team", "team_id": "springfield-isotopes"},
		Confidence: 0.9, Occurrences: 1, ExtractorVersion: "test", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Upsert() error: %v", err)
	}

	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://example.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newReviewCommand()
	cmd.SetArgs([]string{
		"--dictionary", dictPath,
		"--manual", manualPath,
	})
	cmd.SetIn(strings.NewReader("a\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review Execute() error = %v\noutput:\n%s", err, out.String())
	}

	p, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if p.Status != entityproposal.StatusApproved {
		t.Fatalf("proposal status = %s, want approved", p.Status)
	}
	if strings.Contains(out.String(), id) {
		t.Fatalf("review output contains proposal ID %q; the user should decide by displayed proposal, not paste IDs\n%s", id, out.String())
	}
}

func TestReviewCommand_ApprovalFailureKeepsSessionInteractive(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Alias Without Target", NormalizedName: "alias without target",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewAlias,
		Confidence: 0.9, Occurrences: 1, ExtractorVersion: "test", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Upsert() error: %v", err)
	}

	dictPath, manualPath := writeApproveTestCatalogFiles(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://example.invalid")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newReviewCommand()
	cmd.SetArgs([]string{"--dictionary", dictPath, "--manual", manualPath})
	cmd.SetIn(strings.NewReader("a\nr\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("review Execute() error = %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "remains pending") {
		t.Fatalf("review output = %q, want approval failure to leave the proposal pending for another action", out.String())
	}

	p, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if p.Status != entityproposal.StatusRejected {
		t.Fatalf("proposal status = %s, want rejected after the follow-up action", p.Status)
	}
}

func TestPrintProposalForReview_ShowsVerifiedIdentityAndWebEvidence(t *testing.T) {
	p := entityproposal.Proposal{
		CanonicalName: "Patrick Ostrander", EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata:   map[string]string{"position": "QB", "team": "Capital City Capitals", "league": "NFL"},
		WebSources: []entityproposal.WebSource{{Domain: "league.example.com"}, {Domain: "capitals.example.com"}, {Domain: "league.example.com"}},
	}
	cmd := newReviewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	printProposalForReview(cmd, 1, 1, p)
	for _, want := range []string{"Position: QB", "Team: Capital City Capitals", "League: NFL", "Web evidence: league.example.com, capitals.example.com"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Role:") {
		t.Errorf("output has Role line without role metadata:\n%s", out.String())
	}
}

func TestPrintProposalForReview_DistinguishesFreeAgentFromUnknown(t *testing.T) {
	render := func(md map[string]string) string {
		p := entityproposal.Proposal{
			CanonicalName: "Example Player", EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Metadata: md,
		}
		cmd := newReviewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		printProposalForReview(cmd, 1, 1, p)
		return out.String()
	}
	fa := render(map[string]string{"position": "WR", "league": "NFL", "affiliation_status": "free_agent"})
	if !strings.Contains(fa, "Affiliation: FREE AGENT") || strings.Contains(fa, "UNKNOWN") || strings.Contains(fa, "Team:") {
		t.Errorf("free agent output:\n%s", fa)
	}
	for name, md := range map[string]map[string]string{
		"explicit": {"position": "WR", "league": "NFL", "affiliation_status": "unknown"},
		"absent":   {"position": "WR", "league": "NFL"},
	} {
		got := render(md)
		if !strings.Contains(got, "Affiliation: UNKNOWN") || strings.Contains(got, "FREE AGENT") {
			t.Errorf("%s output:\n%s", name, got)
		}
	}
}
