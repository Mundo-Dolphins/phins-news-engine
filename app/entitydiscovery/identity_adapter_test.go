package entitydiscovery

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

func TestVerifyResultFromIdentity(t *testing.T) {
	md := map[string]string{"provider": "wikidata", "provider_id": "Q1", "league": "NFL"}
	tests := []struct {
		status   entityidentity.Status
		want     weblookup.Disposition
		resolved bool
	}{
		{entityidentity.StatusVerifiedNFL, weblookup.DispositionKeep, true},
		{entityidentity.StatusVerifiedNonNFL, weblookup.DispositionReject, true},
		{entityidentity.StatusAmbiguous, weblookup.DispositionUncertain, false},
		{entityidentity.StatusNotFound, weblookup.DispositionUncertain, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			vr := VerifyResultFromIdentity(entityidentity.Result{
				Status: tt.status, Reason: "r", CanonicalName: "X Y",
				EntityType: entityproposal.EntityTypePlayer, Metadata: md,
			})
			if vr.Disposition != tt.want || vr.Resolved != tt.resolved {
				t.Fatalf("got %s/%v, want %s/%v", vr.Disposition, vr.Resolved, tt.want, tt.resolved)
			}
			if tt.resolved && (vr.CanonicalName != "X Y" || vr.Metadata["league"] != "NFL") {
				t.Errorf("identity not carried: %+v", vr)
			}
			if !tt.resolved && (vr.CanonicalName != "" || vr.Metadata != nil) {
				t.Errorf("unresolved result must carry no identity: %+v", vr)
			}
		})
	}
}

func TestVerifyResultFromIdentity_DoesNotAliasMetadata(t *testing.T) {
	md := map[string]string{"league": "NFL"}
	vr := VerifyResultFromIdentity(entityidentity.Result{Status: entityidentity.StatusVerifiedNFL, Metadata: md})
	vr.Metadata["league"] = "changed"
	if md["league"] != "NFL" {
		t.Fatal("adapter shared the metadata map")
	}
}

func TestCatalogIndex_IdentityCatalog(t *testing.T) {
	idx := BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Isotopes", "Isotopes"}},
	})
	ce, ok := idx.IdentityCatalog().Lookup("isotopes")
	if !ok || ce.ID != "springfield-isotopes" || ce.Kind != entityproposal.EntityTypeTeam || ce.CanonicalName != "Springfield Isotopes" {
		t.Fatalf("lookup = %+v, %v", ce, ok)
	}
	if _, ok := idx.IdentityCatalog().Lookup("springfield"); ok {
		t.Fatal("unknown term must not resolve")
	}
}
