// app/entitydiscovery/reverify.go
package entitydiscovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
)

// MetadataVerifyAttemptedAt is the proposal metadata key (RFC3339 UTC)
// recording that ReverifyPending already attempted this proposal. It is
// the resume cursor: attempted proposals are skipped unless
// ReverifyOptions.RetryAttempted is set.
const MetadataVerifyAttemptedAt = "verify_attempted_at"

// MetadataVerifyAttemptedVersion records the WikidataVerificationVersion
// current when the attempt was made, so a version bump re-attempts rows
// instead of treating the older marker as final.
const MetadataVerifyAttemptedVersion = "verify_attempted_version"

// verifyOutcome is what verifyAndPersist did with one proposal.
type verifyOutcome int

const (
	outcomeKept verifyOutcome = iota
	outcomeAutoRejected
	outcomeUncertain
	outcomeConflict
	outcomeDuplicate
	outcomeProviderError
)

// promptExcludedMetadata are bookkeeping keys never sent to the verifier.
var promptExcludedMetadata = map[string]bool{MetadataVerifyAttemptedAt: true, MetadataVerifyAttemptedVersion: true, "reason": true}

func candidateMetadata(md map[string]string) map[string]string {
	out := make(map[string]string, len(md))
	for k, v := range md {
		if !promptExcludedMetadata[k] {
			out[k] = v
		}
	}
	return out
}

// verifyAndPersist is the Upsert-independent verification step shared by
// Discover and ReverifyPending. candidate is what the verifier is asked
// about; merged is the stored row the outcome is applied to (so
// UpdateVerification's wholesale replace cannot lose accumulated state).
// When dryRun is true nothing is written but the outcome is still reported.
// A non-empty attemptedAt is stamped into metadata on every attempted
// outcome (including uncertain and conflict, written as a marker-only update).
func verifyAndPersist(ctx context.Context, repo entityproposalport.EntityProposalRepository, verifier Verifier, loader ArticleTextLoader, catalog CatalogIndex, knownTeamIDs map[string]bool, candidate, merged entityproposal.Proposal, dryRun bool, att attempt) (verifyOutcome, error) {
	var texts []ArticleText
	if loader != nil && len(candidate.Sources) > 0 {
		ids := make([]string, 0, len(candidate.Sources))
		for _, src := range candidate.Sources {
			ids = append(ids, src.ArticleID)
		}
		if got, err := loader.Texts(ctx, ids); err == nil {
			texts = got
		}
	}
	vr := verifier.Verify(ctx, VerifyCandidate{
		Name: candidate.CanonicalName, Type: candidate.EntityType, Metadata: candidateMetadata(candidate.Metadata),
		Confidence: candidate.Confidence, Sources: candidate.Sources, Texts: texts,
	})
	if merged.Verification.Provider == "wikidata" && vr.Provenance.Provider != "wikidata" {
		// Authority order: a decisive Wikidata identity outranks web/LLM
		// interpretation, so a fallback result never overrides it.
		vr = VerifyResult{Disposition: weblookup.DispositionUncertain}
	}
	if vr.Disposition == weblookup.DispositionUncertain && vr.ProviderError {
		// A provider outage is not evidence: leave the row unmarked so a
		// later run retries it instead of dropping it from the backlog.
		return outcomeProviderError, nil
	}
	if vr.Disposition == weblookup.DispositionUncertain {
		if err := writeMarker(ctx, repo, merged, dryRun, att, vr.Suggestions); err != nil {
			return outcomeUncertain, err
		}
		return outcomeUncertain, nil
	}
	verified := ApplyVerification(merged, vr, catalog, knownTeamIDs)
	if verified.Status == entityproposal.StatusPending && verified.Confidence < merged.Confidence {
		verified.Confidence = merged.Confidence
	}
	verified.ID = merged.ID
	if att.at != "" {
		stampMarker(&verified, att)
	}
	if !dryRun {
		if err := repo.UpdateVerification(ctx, verified); err != nil {
			if errors.Is(err, entityproposal.ErrConflict) {
				if verified.Status == entityproposal.StatusPending && markDuplicate(ctx, repo, merged, verified, att) {
					return outcomeDuplicate, nil
				}
				if err := writeMarker(ctx, repo, merged, dryRun, att, nil); err != nil {
					return outcomeConflict, err
				}
				return outcomeConflict, nil
			}
			return outcomeUncertain, fmt.Errorf("entitydiscovery: update verification %q: %w", candidate.CanonicalName, err)
		}
	}
	if verified.Status == entityproposal.StatusRejected {
		return outcomeAutoRejected, nil
	}
	return outcomeKept, nil
}

// markDuplicate handles a Keep whose corrected identity collides with
// another pending proposal: the still-pending row is rejected as
// duplicate_of_existing under its pre-verification identity (so a later
// Upsert of the same surface form is absorbed instead of re-queued),
// keeping the verifier's web evidence. It reports whether the row was
// marked; a row decided meanwhile, or a failed write, is left alone.
func markDuplicate(ctx context.Context, repo entityproposalport.EntityProposalRepository, merged, verified entityproposal.Proposal, att attempt) bool {
	row, err := repo.Get(ctx, merged.ID)
	if err != nil || row.Status != entityproposal.StatusPending {
		return false
	}
	if len(verified.WebSources) > 0 {
		row.WebSources = verified.WebSources
	}
	row.Verification = verified.Verification
	row.Status = entityproposal.StatusRejected
	row.DecisionReason = entityproposal.ReasonDuplicateOfExisting
	if att.at != "" {
		stampMarker(&row, att)
	}
	return repo.UpdateVerification(ctx, row) == nil
}

// attempt is the marker stamped on an attempted proposal. version is the
// WikidataVerificationVersion, set only when the Wikidata stage ran, so a
// run without it never claims a Wikidata attempt.
type attempt struct{ at, version string }

func stampMarker(p *entityproposal.Proposal, att attempt) {
	md := make(map[string]string, len(p.Metadata)+2)
	for k, v := range p.Metadata {
		md[k] = v
	}
	md[MetadataVerifyAttemptedAt] = att.at
	if att.version != "" {
		md[MetadataVerifyAttemptedVersion] = att.version
	} else {
		delete(md, MetadataVerifyAttemptedVersion)
	}
	p.Metadata = md
}

// writeMarker persists only the attempt marker on the stored row (status
// and everything else unchanged).
func writeMarker(ctx context.Context, repo entityproposalport.EntityProposalRepository, merged entityproposal.Proposal, dryRun bool, att attempt, suggestions map[string]string) error {
	if dryRun || att.at == "" {
		return nil
	}
	row, err := repo.Get(ctx, merged.ID)
	if err != nil {
		return fmt.Errorf("entitydiscovery: get proposal %q: %w", merged.CanonicalName, err)
	}
	if row.Status != entityproposal.StatusPending {
		return nil
	}
	if suggestions != nil {
		row = ApplySuggestions(row, suggestions)
	}
	stampMarker(&row, att)
	if err := repo.UpdateVerification(ctx, row); err != nil && !errors.Is(err, entityproposal.ErrConflict) {
		return fmt.Errorf("entitydiscovery: record attempt %q: %w", merged.CanonicalName, err)
	}
	return nil
}

// ReverifyOptions configures one `entities proposals verify` run.
type ReverifyOptions struct {
	// Limit bounds how many proposals are sent to the verifier (0 = no cap).
	Limit int
	// EntityType, when non-empty, restricts the run to that type.
	EntityType string
	// DryRun reports outcomes without writing proposals.
	DryRun bool
	// RetryAttempted ignores the attempt marker so previously attempted
	// (e.g. uncertain) proposals are verified again, oldest attempt first.
	RetryAttempted bool
	// Texts, when set, supplies each candidate's source article text.
	Texts ArticleTextLoader
	// Wikidata reports that the Wikidata stage is active: attempts are
	// stamped with the resolver version, and attempts that predate versioned
	// markers no longer count as done.
	Wikidata bool
	// Now stamps the attempt marker; zero means time.Now().
	Now time.Time
}

// ReverifyResult summarizes one run. Processed = Kept + AutoRejected +
// Uncertain + Conflicts + Duplicates + ProviderErrors (outage: row left unmarked); Skipped counts eligible proposals not sent to the
// verifier because they were already verified or attempted.
type ReverifyResult struct {
	Processed, Kept, AutoRejected, Uncertain, Conflicts, Duplicates, ProviderErrors, Skipped int
}

// alreadyVerified reports whether a proposal was verified by an earlier
// run: kept by a decisive Wikidata identity at the current resolver version
// (never handed to the LLM fallback; authority order), or verified by
// Discover (web sources and a resolved league), or attempted by a run of
// the current version (attempt marker). A Wikidata row stamped by an older
// version, and an attempt made under an older version, are verified again.
func alreadyVerified(p entityproposal.Proposal, retryAttempted, wikidataStage bool) bool {
	wikidata := p.Verification.Provider == "wikidata"
	stale := wikidata && p.Verification.Version != WikidataVerificationVersion
	if wikidata && !stale {
		return true
	}
	if !wikidata && len(p.WebSources) > 0 && p.Metadata["league"] != "" {
		return true
	}
	if retryAttempted || p.Metadata[MetadataVerifyAttemptedAt] == "" {
		return false
	}
	v := p.Metadata[MetadataVerifyAttemptedVersion]
	return v == WikidataVerificationVersion || (v == "" && !stale && !wikidataStage)
}

// ReverifyPending verifies the pending new_entity/possible_duplicate
// backlog using each proposal's stored Sources (no article re-fetch). It
// is idempotent and resumable: every attempt is stamped with
// MetadataVerifyAttemptedAt so later runs move on to the rest of the backlog.
func ReverifyPending(ctx context.Context, repo entityproposalport.EntityProposalRepository, verifier Verifier, catalog CatalogIndex, opts ReverifyOptions) (ReverifyResult, error) {
	var result ReverifyResult
	pending, err := repo.List(ctx, opts.EntityType, string(entityproposal.StatusPending))
	if err != nil {
		return result, fmt.Errorf("entitydiscovery: list pending proposals: %w", err)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	att := attempt{at: now.UTC().Format(time.RFC3339)}
	if opts.Wikidata {
		att.version = WikidataVerificationVersion
	}
	var todo []entityproposal.Proposal
	for _, p := range pending {
		if p.Kind != entityproposal.KindNewEntity && p.Kind != entityproposal.KindPossibleDuplicate {
			continue
		}
		if opts.EntityType != "" && string(p.EntityType) != opts.EntityType {
			continue
		}
		if alreadyVerified(p, opts.RetryAttempted, opts.Wikidata) {
			result.Skipped++
			continue
		}
		todo = append(todo, p)
	}
	if opts.RetryAttempted {
		// Oldest attempt first (never-attempted first of all), so repeated
		// --retry-attempted runs with a --limit walk the whole backlog
		// instead of re-verifying the same first N proposals.
		sort.SliceStable(todo, func(i, j int) bool {
			return todo[i].Metadata[MetadataVerifyAttemptedAt] < todo[j].Metadata[MetadataVerifyAttemptedAt]
		})
	}
	if opts.Limit > 0 && len(todo) > opts.Limit {
		todo = todo[:opts.Limit]
	}
	knownTeamIDs := catalog.TeamIDs()
	for _, p := range todo {
		out, err := verifyAndPersist(ctx, repo, verifier, opts.Texts, catalog, knownTeamIDs, p, p, opts.DryRun, att)
		if err != nil {
			return result, err
		}
		result.Processed++
		switch out {
		case outcomeKept:
			result.Kept++
		case outcomeAutoRejected:
			result.AutoRejected++
		case outcomeUncertain:
			result.Uncertain++
		case outcomeConflict:
			result.Conflicts++
		case outcomeDuplicate:
			result.Duplicates++
		case outcomeProviderError:
			result.ProviderErrors++
		}
	}
	return result, nil
}
