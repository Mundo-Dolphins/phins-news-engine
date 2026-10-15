// app/entitydiscovery/verify.go
package entitydiscovery

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ArticleText is the stored text of one source article.
type ArticleText struct{ ID, Title, Summary string }

// ArticleTextLoader fetches the text of source articles by id; missing
// articles are skipped.
type ArticleTextLoader interface {
	Texts(ctx context.Context, articleIDs []string) ([]ArticleText, error)
}

// VerifyCandidate is one uncertain candidate's current state, handed to
// a Verifier for a bounded web-lookup pass.
type VerifyCandidate struct {
	Name       string
	Type       entityproposal.EntityType
	Metadata   map[string]string
	Confidence float64
	Sources    []entityproposal.Source
	// Texts is the stored title+summary of the source articles, when a loader
	// was available.
	Texts           []ArticleText
	FallbackHistory []appout.FallbackSignal
}

// VerifyDisposition is the verifier's verdict (keep, reject, uncertain).
type VerifyDisposition = weblookup.Disposition

// Provenance identifies the verifier behind a VerifyResult.
type Provenance struct {
	Provider   string
	ProviderID string
	Version    string
	Reason     string
}

func (p Provenance) toVerification() entityproposal.Verification {
	return entityproposal.Verification{Provider: p.Provider, ProviderID: p.ProviderID, Version: p.Version}
}

// VerifyResult is a Verifier's outcome for one candidate. Disposition
// Uncertain (Resolved false) means no trustworthy evidence was found;
// callers must leave the proposal unchanged in that case.
type VerifyResult struct {
	Disposition   VerifyDisposition
	CanonicalName string
	EntityType    entityproposal.EntityType
	Metadata      map[string]string
	Confidence    float64
	WebSources    []entityproposal.WebSource
	Reason        string
	Resolved      bool
	Provenance    Provenance
	// ProviderError marks an unresolved result caused by a knowledge-source
	// failure (not by missing evidence): callers must not record the
	// proposal as attempted, so a later run retries it.
	ProviderError bool
	// DecisionReason overrides the reason recorded on a Reject ("" means
	// ReasonAutoRejectedNonNFL).
	DecisionReason string
	// Suggestions are suggested_* metadata a stage proposes without deciding;
	// they are written to the proposal when it stays pending.
	Suggestions map[string]string
}

// Verifier is the optional collaborator Discover calls for a candidate
// ShouldVerify flags as uncertain. A nil Verifier means
// "fast mode": Discover behaves exactly as it did before this feature
// existed. Verify never returns an error — any internal failure must be
// represented as VerifyResult{Resolved: false} (see
// app/catalogverification.Service.Verify, the concrete
// implementation used in production).
type Verifier interface {
	Verify(ctx context.Context, candidate VerifyCandidate) VerifyResult
}

// MetadataAffiliationStatus is the proposal metadata key carrying an
// eventsignals.AffiliationStatus value.
const MetadataAffiliationStatus = "affiliation_status"

// ShouldVerify decides whether a proposal warrants a bounded web-lookup
// pass. new_entity and possible_duplicate always verify; new_alias only
// below minConfidence. Independently of kind, a team (reaching here
// means no exact catalog match), a player missing position/team (a verified free agent needs no team), a
// coach/executive missing role/team, or any proposal missing
// sport/league verifies. Empty metadata values count as absent.
func ShouldVerify(p entityproposal.Proposal, minConfidence float64) bool {
	switch p.Kind {
	case entityproposal.KindNewEntity, entityproposal.KindPossibleDuplicate:
		return true
	case entityproposal.KindNewAlias:
		if p.Confidence < minConfidence {
			return true
		}
	}
	has := func(k string) bool { return p.Metadata[k] != "" }
	switch p.EntityType {
	case entityproposal.EntityTypeTeam:
		return true
	case entityproposal.EntityTypePlayer:
		// A verified free agent legitimately has no team; unknown
		// affiliation stays incomplete so a later pass may improve it.
		freeAgent := p.Metadata[MetadataAffiliationStatus] == string(eventsignals.AffiliationFreeAgent)
		if !has("position") || (!has("team") && !freeAgent) {
			return true
		}
	case entityproposal.EntityTypeCoach, entityproposal.EntityTypeExecutive:
		if !has("role") || !has("team") {
			return true
		}
	}
	return !has("sport") || !has("league")
}
