package entitydiscovery

import (
	"context"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	WikidataVerificationVersion = "wikidata-v1"
	FallbackVerificationVersion = "web-lookup-v1"
)

// ChainStats counts ChainVerifier outcomes.
type ChainStats struct {
	Considered        int // Verify calls
	WikidataNFL       int
	WikidataNonNFL    int
	WikidataAmbiguous int
	WikidataNotFound  int
	WikidataErrors    int
	FallbackCalls     int
	// League counts the league gazetteer stage's outcomes.
	League LeagueStats
}

// StatsReporter exposes ChainStats.
type StatsReporter interface{ ChainStats() ChainStats }

// ChainVerifier verifies candidates against Wikidata first and only falls
// back to another Verifier when Wikidata is not decisive.
type ChainVerifier struct {
	Knowledge     appout.KnowledgeSource // nil = Wikidata-first off
	Fallback      Verifier               // nil = fallback off
	League        *LeagueStage           // nil = league gazetteer off
	Catalog       CatalogIndex
	MaxCandidates int              // <=0 means entityidentity.MaxCandidates
	Now           func() time.Time // nil = time.Now
	Logger        appout.Logger    // nil = silent
	// Resolve is a test seam; nil = entityidentity.Resolve.
	Resolve func(entityidentity.Input) entityidentity.Result

	mu    sync.Mutex
	stats ChainStats
}

// AssessmentError is Assessment.Status when the knowledge source failed.
const AssessmentError = "error"

// Assessment is the Wikidata stage's verdict for one candidate, before any
// fallback. It backs both Verify and read-only evaluation.
type Assessment struct {
	Status     string       // entityidentity.Status value, or AssessmentError
	Result     VerifyResult // Provenance set only when Decisive
	Candidates int          // Wikidata search hits
	Decisive   bool         // verified_nfl or verified_non_nfl
}

// Verify runs the league gazetteer first, then Wikidata, then the web fallback.
func (c *ChainVerifier) Verify(ctx context.Context, cand VerifyCandidate) VerifyResult {
	c.bump(func(s *ChainStats) { s.Considered++ })
	var suggestions map[string]string
	if c.League != nil {
		res, decided := c.League.Verify(ctx, cand)
		if decided {
			if res.Disposition == weblookup.DispositionReject && c.Logger != nil {
				// Closed-set and noise rejects carry no external evidence, so
				// each one is logged by name to make a bad alias rule auditable.
				c.Logger.Info("league gazetteer rejected a proposal", "name", cand.Name, "type", string(cand.Type), "reason", res.DecisionReason)
			}
			return res
		}
		suggestions = res.Suggestions
	}
	vr := c.verifyRest(ctx, cand)
	// A player on the current roster is an NFL player by construction. If the
	// gazetteer abstained (a stale team on the candidate, say) and a later stage
	// says "not NFL", keep the proposal pending instead of rejecting it.
	if c.League != nil && vr.Disposition == weblookup.DispositionReject {
		if p, ok := c.League.RosterPlayer(cand); ok {
			if c.Logger != nil {
				c.Logger.Warn("a later stage rejected a player on the current roster; keeping the proposal pending",
					"name", cand.Name, "stage", vr.Provenance.Provider, "reason", vr.Reason)
			}
			suggestions = c.League.suggest([]leaguegazetteer.Player{p}, "roster_match")
			vr = VerifyResult{Disposition: weblookup.DispositionUncertain, Reason: "a later stage rejected a player on the current roster"}
		}
	}
	if len(suggestions) > 0 && !vr.Resolved {
		vr.Suggestions = suggestions
	}
	return vr
}

// verifyRest runs the Wikidata stage and the web fallback.
func (c *ChainVerifier) verifyRest(ctx context.Context, cand VerifyCandidate) VerifyResult {
	providerErr := false
	if c.Knowledge != nil {
		a := c.Assess(ctx, cand)
		if a.Decisive {
			return c.completeAffiliation(ctx, cand, a.Result)
		}
		providerErr = a.Status == AssessmentError
	}
	vr := c.fallback(ctx, cand)
	if providerErr && !vr.Resolved {
		vr.ProviderError = true
	}
	return vr
}

// completeAffiliation lets the trusted fallback resolve a verified NFL
// player whose Wikidata affiliation is unknown. Only the
// affiliation fields move, and only on positive evidence: a resolved Keep
// backed by at least one trusted web source that names either a team with
// a team_id or an explicit free-agent status. Anything else (no fallback,
// uncertain, an LLM claim without sources) leaves the affiliation unknown.
func (c *ChainVerifier) completeAffiliation(ctx context.Context, cand VerifyCandidate, wd VerifyResult) VerifyResult {
	if c.Fallback == nil || wd.Disposition != weblookup.DispositionKeep ||
		wd.EntityType != entityproposal.EntityTypePlayer ||
		wd.Metadata[MetadataAffiliationStatus] != string(eventsignals.AffiliationUnknown) {
		return wd
	}
	c.bump(func(s *ChainStats) { s.FallbackCalls++ })
	fb := c.Fallback.Verify(ctx, cand)
	if !fb.Resolved || fb.Disposition != weblookup.DispositionKeep || len(fb.WebSources) == 0 {
		return wd
	}
	switch status := fb.Metadata[MetadataAffiliationStatus]; {
	case status == string(eventsignals.AffiliationTeam) && fb.Metadata["team"] != "" && fb.Metadata["team_id"] != "":
		wd.Metadata["team"], wd.Metadata["team_id"] = fb.Metadata["team"], fb.Metadata["team_id"]
	case status == string(eventsignals.AffiliationFreeAgent) && fb.Metadata["team"] == "" && fb.Metadata["team_id"] == "":
		delete(wd.Metadata, "team")
		delete(wd.Metadata, "team_id")
	default:
		return wd
	}
	wd.Metadata[MetadataAffiliationStatus] = fb.Metadata[MetadataAffiliationStatus]
	wd.WebSources = append([]entityproposal.WebSource(nil), fb.WebSources...)
	return wd
}

// Assess runs only the Wikidata stage (c.Knowledge must be non-nil). Every
// non-decisive outcome, including errors, is counted and reported so the
// caller may fall back; Assess itself never does.
func (c *ChainVerifier) Assess(ctx context.Context, cand VerifyCandidate) Assessment {
	limit := c.MaxCandidates
	if limit <= 0 {
		limit = entityidentity.MaxCandidates
	}
	cands, err := c.Knowledge.Search(ctx, cand.Name, limit)
	if err != nil {
		return c.knowledgeError("search", cand.Name, err)
	}
	entities := map[string]knowledge.Entity{}
	if len(cands) > 0 {
		ids := make([]string, len(cands))
		for i, k := range cands {
			ids[i] = k.ID
		}
		got, err := c.Knowledge.Get(ctx, ids)
		if err != nil {
			return c.knowledgeError("get", cand.Name, err)
		}
		var refs []string
		for _, e := range got {
			entities[e.ID] = e
			refs = append(refs, entityidentity.ReferencedIDs(e)...)
		}
		if refs = dedupeMissing(refs, entities); len(refs) > 0 {
			teams, err := c.Knowledge.Get(ctx, refs)
			if err != nil {
				return c.knowledgeError("get teams", cand.Name, err)
			}
			for _, e := range teams {
				entities[e.ID] = e
			}
		}
	}
	resolve := c.Resolve
	if resolve == nil {
		resolve = entityidentity.Resolve
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	res := resolve(entityidentity.Input{
		Name: cand.Name, ExpectedKind: cand.Type, Catalog: c.Catalog.IdentityCatalog(),
		Candidates: cands, Entities: entities, Now: now(),
	})
	switch res.Status {
	case entityidentity.StatusVerifiedNFL:
		c.bump(func(s *ChainStats) { s.WikidataNFL++ })
	case entityidentity.StatusVerifiedNonNFL:
		c.bump(func(s *ChainStats) { s.WikidataNonNFL++ })
	case entityidentity.StatusAmbiguous:
		c.bump(func(s *ChainStats) { s.WikidataAmbiguous++ })
	default:
		c.bump(func(s *ChainStats) { s.WikidataNotFound++ })
	}
	a := Assessment{Status: string(res.Status), Candidates: len(cands), Result: VerifyResultFromIdentity(res)}
	if !a.Result.Resolved {
		return a
	}
	reason := res.Reason
	if res.AffiliationReason != "" {
		reason += "; " + res.AffiliationReason
	}
	a.Result.Provenance = Provenance{
		Provider: "wikidata", ProviderID: res.Metadata["provider_id"],
		Version: WikidataVerificationVersion, Reason: reason,
	}
	a.Decisive = true
	return a
}

func (c *ChainVerifier) knowledgeError(op, name string, err error) Assessment {
	c.bump(func(s *ChainStats) { s.WikidataErrors++ })
	if c.Logger != nil {
		c.Logger.Warn("wikidata verification failed; falling back", "op", op, "candidate", name, "error", err.Error())
	}
	return Assessment{Status: AssessmentError}
}

func (c *ChainVerifier) fallback(ctx context.Context, cand VerifyCandidate) VerifyResult {
	if c.Fallback == nil {
		return VerifyResult{Disposition: weblookup.DispositionUncertain, Reason: "no verifier available"}
	}
	c.bump(func(s *ChainStats) { s.FallbackCalls++ })
	vr := c.Fallback.Verify(ctx, cand)
	if vr.Resolved || vr.Disposition != weblookup.DispositionUncertain {
		vr.Provenance = Provenance{Provider: "llm_web", Version: FallbackVerificationVersion}
	}
	return vr
}

// dedupeMissing returns ids not yet in have, without duplicates.
func dedupeMissing(ids []string, have map[string]knowledge.Entity) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if _, ok := have[id]; ok || seen[id] || id == "" {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (c *ChainVerifier) bump(f func(*ChainStats)) { c.mu.Lock(); f(&c.stats); c.mu.Unlock() }

func (c *ChainVerifier) ChainStats() ChainStats {
	c.mu.Lock()
	st := c.stats
	c.mu.Unlock()
	if c.League != nil {
		st.League = c.League.Stats()
	}
	return st
}
