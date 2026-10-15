package entitydiscovery

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// identityCatalog adapts CatalogIndex to entityidentity.Catalog.
type identityCatalog struct{ idx CatalogIndex }

func (c identityCatalog) Lookup(normalizedName string) (entityidentity.CatalogEntry, bool) {
	ce, ok := c.idx.Lookup(normalizedName)
	if !ok {
		return entityidentity.CatalogEntry{}, false
	}
	return entityidentity.CatalogEntry{ID: ce.ID, CanonicalName: ce.CanonicalName, Kind: ce.Type}, true
}

// IdentityCatalog exposes the index to the deterministic knowledge-source
// scorer.
func (idx CatalogIndex) IdentityCatalog() entityidentity.Catalog { return identityCatalog{idx} }

// VerifyResultFromIdentity converts a deterministic knowledge-source
// verdict into a VerifyResult that ApplyVerification already understands:
// verified_nfl keeps, verified_non_nfl rejects, and ambiguous/not_found are
// unresolved so the fallback verifier may still try. Unresolved results
// carry no identity or metadata.
func VerifyResultFromIdentity(r entityidentity.Result) VerifyResult {
	switch r.Status {
	case entityidentity.StatusVerifiedNFL, entityidentity.StatusVerifiedNonNFL:
		d := weblookup.DispositionKeep
		if r.Status == entityidentity.StatusVerifiedNonNFL {
			d = weblookup.DispositionReject
		}
		md := make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			md[k] = v
		}
		return VerifyResult{
			Disposition: d, CanonicalName: r.CanonicalName, EntityType: r.EntityType,
			Metadata: md, Reason: r.Reason, Resolved: true,
		}
	}
	return VerifyResult{Disposition: weblookup.DispositionUncertain, Reason: r.Reason}
}
