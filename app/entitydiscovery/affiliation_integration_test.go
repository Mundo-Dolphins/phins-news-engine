package entitydiscovery_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
)

// Each affiliation outcome a verifier can emit goes
// through the real ApplyVerification and ApplyApproval path.
func verifiedProposal(md map[string]string) entityproposal.Proposal {
	base := entityproposal.Proposal{
		Kind: entityproposal.KindNewEntity, EntityType: entityproposal.EntityTypePlayer,
		CanonicalName: "Pat Test", NormalizedName: "pat test", Metadata: map[string]string{},
	}
	vr := entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, EntityType: entityproposal.EntityTypePlayer,
		CanonicalName: "Pat Test", Metadata: md,
	}
	known := map[string]bool{"capital-city-capitals": true, "springfield-isotopes": true}
	return entitydiscovery.ApplyVerification(base, vr, entitydiscovery.BuildCatalogIndex(nil), known)
}

func manualEntry(t *testing.T, paths entitydiscovery.CatalogPaths, id string) (eventsignals.EntityRule, bool) {
	t.Helper()
	entries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return eventsignals.EntityRule{}, false
}

func TestAffiliationPersistence_TeamBecomesKnownTeamCatalogEntry(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := verifiedProposal(map[string]string{
		"affiliation_status": "team", "team": "Capital City Capitals", "team_id": "capital-city-capitals", "position": "QB",
	})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	e, ok := manualEntry(t, paths, "pat-test")
	if !ok || e.AffiliationStatus != eventsignals.AffiliationTeam || e.TeamID != "capital-city-capitals" {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestAffiliationPersistence_FreeAgentBecomesLeagueEntryWithoutTeam(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	p := verifiedProposal(map[string]string{"affiliation_status": "free_agent", "position": "WR"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	e, ok := manualEntry(t, paths, "pat-test")
	if !ok || e.AffiliationStatus != eventsignals.AffiliationFreeAgent || e.TeamID != "" {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestAffiliationPersistence_UnknownCannotEnterIsotopesRoster(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	beforeD, beforeM := dictBytes(t, paths)
	p := verifiedProposal(map[string]string{"affiliation_status": "unknown", "position": "WR"})
	if err := entitydiscovery.ApplyApproval(p, paths, time.Now().UTC()); err == nil {
		t.Fatal("unknown affiliation was approved")
	}
	afterD, afterM := dictBytes(t, paths)
	if beforeD != afterD || beforeM != afterM {
		t.Fatal("catalog files changed for an unknown affiliation")
	}
}
