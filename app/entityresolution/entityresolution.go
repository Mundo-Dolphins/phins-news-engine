// Package entityresolution orchestrates the online, per-candidate LLM
// fallback for unknown entities: given the
// eventsignals.UnknownEntity candidates one article's Extract call
// reported, resolve each distinct one (deduplicated by NormalizedText),
// checking ports/entityproposalport's
// EntityProposalRepository as a cache before ever calling the LLM, and
// recording a successful resolution back into it as evidence for the
// offline entities review CLI to eventually approve into
// the deterministic catalog.
//
// Unlike app/entitydiscovery, this package is deliberately imported
// by the ingestion binary — that is the whole point of an online
// fallback. Every failure mode (LLM error, cache error, validation
// failure) degrades to "this candidate stays unresolved"; Resolve never
// returns an error, by design, so ProcessCycle.cluster never needs error
// handling for this call.
package entityresolution

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	domainresolution "github.com/Mundo-Dolphins/phins-news-engine/domain/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ResolvedEntity is one unknown-entity candidate the fallback
// successfully resolved and validated for the current article.
type ResolvedEntity struct {
	CandidateText  string
	NormalizedText string
	Type           string // player|coach|executive|team
	CanonicalName  string
	Confidence     float64
}

// Deps are the fallback's collaborators, all optional-safe: ProcessCycle
// only constructs a non-nil Deps when entity_extraction.fallback.enabled
// is true.
type Deps struct {
	LLM           out.EntityResolutionLLM
	Cache         entityproposalport.EntityProposalRepository
	MinConfidence float64
	Logger        out.Logger
	// Timeout is a hard wall-clock budget for one candidate, including cache
	// lookups and the LLM call. It is enforced here as well as in the HTTP
	// client so an alternative resolver cannot stall ingestion by ignoring
	// context cancellation. Zero retains the legacy unbounded behaviour.
	Timeout time.Duration
	// MaxCandidatesPerArticle caps how many distinct (by NormalizedText)
	// candidates Resolve will process for a single article; 0 means no
	// cap. Bounds the worst case where an article reports many unknown
	// spans against a slow/unreachable LLM endpoint from holding the
	// per-article transaction open for an unbounded number of LLM round
	// trips (Finding 4 of the 2026-09-16 final review). Candidates beyond
	// the cap are silently skipped (left unresolved), with a single Warn
	// log line naming how many were skipped.
	MaxCandidatesPerArticle int
}

// candidateEntityTypes is the fixed, small set FindByNormalizedName is
// probed against — the cache lookup doesn't know a candidate's type in
// advance (that's exactly what resolution decides), so it checks all
// four known types. Four indexed lookups per distinct candidate is
// negligible next to an LLM round trip, and keeps entity_proposals'
// (normalized_name, entity_type) compound key intact rather than
// introducing a name-only collision risk (see the design spec §3).
var candidateEntityTypes = []entityproposal.EntityType{
	entityproposal.EntityTypePlayer,
	entityproposal.EntityTypeCoach,
	entityproposal.EntityTypeExecutive,
	entityproposal.EntityTypeTeam,
}

// Resolve attempts to resolve every distinct (by NormalizedText)
// candidate in unknowns for the article identified by articleID.
// articleText maps "title"|"description"|"body" to that field's full
// text, used as each candidate's LLM Context. Never returns an error:
// any failure for an individual candidate (cache error, LLM error,
// validation failure) is logged at Warn and that candidate is simply
// left out of the result.
func Resolve(ctx context.Context, articleID string, unknowns []eventsignals.UnknownEntity, articleText map[string]string, deps Deps, now time.Time) []ResolvedEntity {
	if len(unknowns) > 0 {
		deps.Logger.Info("entity resolution fallback: triggered", "article_id", articleID, "candidates", len(unknowns))
	}
	seen := map[string]bool{}
	var resolved []ResolvedEntity
	distinctCount := 0
	skipped := 0
	for _, u := range unknowns {
		if seen[u.NormalizedText] {
			continue
		}
		seen[u.NormalizedText] = true
		distinctCount++
		if deps.MaxCandidatesPerArticle > 0 && distinctCount > deps.MaxCandidatesPerArticle {
			skipped++
			continue
		}

		candidateCtx, cancel := candidateContext(ctx, deps.Timeout)
		if cached, ok := lookupCache(candidateCtx, deps.Cache, u.NormalizedText, deps.MinConfidence, deps.Logger); ok {
			cancel()
			deps.Logger.Info("entity resolution fallback: cache hit", "article_id", articleID)
			resolved = append(resolved, ResolvedEntity{
				CandidateText: u.Text, NormalizedText: u.NormalizedText,
				Type: string(cached.EntityType), CanonicalName: cached.CanonicalName, Confidence: cached.Confidence,
			})
			continue
		}

		candidate := domainresolution.Candidate{Text: u.Text, NormalizedText: u.NormalizedText, Field: u.Field, Context: articleText[u.Field]}
		result, err := resolveWithinBudget(candidateCtx, deps.LLM, out.EntityResolutionCandidate{
			Text: candidate.Text, NormalizedText: candidate.NormalizedText, Field: candidate.Field, Context: candidate.Context,
		})
		cancel()
		if err != nil {
			deps.Logger.Warn("entity resolution fallback: LLM call failed", "article_id", articleID, "candidate", u.Text, "error", err)
			// A provider outage should not turn into one slow call per unknown
			// span while this article's transaction remains open. Fail open for
			// the rest of this article after its first unavailable call.
			if distinctCount < len(unknowns) {
				deps.Logger.Warn("entity resolution fallback: provider unavailable; skipping remaining candidates", "article_id", articleID)
			}
			break
		}
		domainResult := domainresolution.Resolution{Resolved: result.Resolved, Type: result.Type, CanonicalName: result.CanonicalName, Confidence: result.Confidence}
		if err := domainresolution.Validate(candidate, domainResult, deps.MinConfidence); err != nil {
			deps.Logger.Warn("entity resolution fallback: validation rejected result", "article_id", articleID, "candidate", u.Text, "error", err)
			continue
		}
		if !domainResult.Resolved {
			continue
		}

		deps.Logger.Info("entity resolution fallback: resolution accepted", "article_id", articleID, "type", domainResult.Type)
		re := ResolvedEntity{
			CandidateText: u.Text, NormalizedText: u.NormalizedText,
			Type: domainResult.Type, CanonicalName: domainResult.CanonicalName, Confidence: domainResult.Confidence,
		}
		resolved = append(resolved, re)
		persistEvidence(ctx, deps.Cache, articleID, re, now, deps.Logger)
	}
	if skipped > 0 {
		deps.Logger.Warn("entity resolution fallback: max candidates per article exceeded, skipping remainder",
			"article_id", articleID, "max_candidates_per_article", deps.MaxCandidatesPerArticle, "skipped_count", skipped)
	}
	return resolved
}

func candidateContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// resolveWithinBudget returns as soon as the candidate deadline expires,
// including when an injected resolver does not cooperate with ctx.Done().
// The buffered channel lets a late result be discarded safely.
func resolveWithinBudget(ctx context.Context, llm out.EntityResolutionLLM, candidate out.EntityResolutionCandidate) (out.EntityResolutionResult, error) {
	type outcome struct {
		result out.EntityResolutionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := llm.Resolve(ctx, candidate)
		done <- outcome{result: result, err: err}
	}()

	select {
	case got := <-done:
		return got.result, got.err
	case <-ctx.Done():
		return out.EntityResolutionResult{}, fmt.Errorf("entity resolution deadline: %w", ctx.Err())
	}
}

func lookupCache(ctx context.Context, cache entityproposalport.EntityProposalRepository, normalizedText string, minConfidence float64, logger out.Logger) (entityproposal.Proposal, bool) {
	for _, t := range candidateEntityTypes {
		p, ok, err := cache.FindByNormalizedName(ctx, normalizedText, t)
		if err != nil {
			logger.Warn("entity resolution fallback: cache lookup failed", "normalized_text", normalizedText, "entity_type", t, "error", err)
			continue
		}
		if ok && isTrustedCacheHit(p, minConfidence) {
			return p, true
		}
	}
	// FindByNormalizedName misses when the cached proposal is keyed by
	// the LLM's canonical name (e.g. "random newcomer") rather than this
	// candidate's own raw span (e.g. "sign random newcomer") —
	// persistEvidence records the candidate's NormalizedText as an alias
	// precisely so a later occurrence of the SAME raw span still hits the
	// cache here.
	for _, t := range candidateEntityTypes {
		p, ok, err := cache.FindByAlias(ctx, normalizedText, t)
		if err != nil {
			logger.Warn("entity resolution fallback: alias cache lookup failed", "normalized_text", normalizedText, "entity_type", t, "error", err)
			continue
		}
		if ok && isTrustedCacheHit(p, minConfidence) {
			return p, true
		}
	}
	return entityproposal.Proposal{}, false
}

// isTrustedCacheHit decides whether a cached proposal found by
// lookupCache is safe to reuse as a resolution outright, rather than
// falling through to call the LLM as if nothing were cached (Finding 2
// of the 2026-09-16 final review): a human-REJECTED proposal must not be
// silently reused as a positive resolution (that inverts the human's
// decision); a proposal below the fallback's own confidence floor must
// not bypass that floor just because it came from the cache instead of a
// fresh LLM call; and a proposal that isn't Kind == KindNewEntity (e.g.
// KindNewAlias, KindPossibleDuplicate) isn't a straightforward resolved
// entity and must not be treated as one.
func isTrustedCacheHit(p entityproposal.Proposal, minConfidence float64) bool {
	if p.Status == entityproposal.StatusRejected {
		return false
	}
	if p.Confidence < minConfidence {
		return false
	}
	if p.Kind != entityproposal.KindNewEntity {
		return false
	}
	return true
}

func persistEvidence(ctx context.Context, cache entityproposalport.EntityProposalRepository, articleID string, re ResolvedEntity, now time.Time, logger out.Logger) {
	normalizedName := entityproposal.Normalize(re.CanonicalName)
	var aliases []string
	// Record the candidate's own raw span as an alias — unless it's
	// already identical to the canonical name's normalized form — so a
	// later occurrence of the SAME raw span hits lookupCache's
	// FindByAlias path even though this proposal is keyed by the LLM's
	// canonical name (see Finding 1 of the 2026-09-16 final review).
	if re.NormalizedText != normalizedName {
		aliases = []string{re.NormalizedText}
	}
	p := entityproposal.Proposal{
		ID:               "", // sqliteentityproposal.Upsert assigns one when inserting
		CanonicalName:    re.CanonicalName,
		NormalizedName:   normalizedName,
		EntityType:       entityproposal.EntityType(re.Type),
		Kind:             entityproposal.KindNewEntity,
		Metadata:         map[string]string{},
		Confidence:       re.Confidence,
		Occurrences:      1,
		Aliases:          aliases,
		Sources:          []entityproposal.Source{{ArticleID: articleID}},
		FirstSeen:        now,
		LastSeen:         now,
		ExtractorVersion: "online-fallback-v1",
		CreatedAt:        now,
		Source:           entityproposal.ProposalSourceOnlineFallback,
	}
	if _, err := cache.Upsert(ctx, p); err != nil {
		logger.Warn("entity resolution fallback: failed to record evidence", "article_id", articleID, "candidate", re.CandidateText, "error", err)
	}
}
