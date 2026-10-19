// Package usecase implements the driving ports declared in
// application/ports/in against the driven ports declared in
// application/ports/out.
package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustergate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/similarity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/storyguard"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ProcessCycle fetches candidate articles and runs each one through
// validation, URL/text normalization, a cascading exact/near-duplicate
// check, classification (still a placeholder — see the comment in
// processOne), event clustering, editorial-quality classification
// (disabled/shadow/enforce — see Step 7 in processOne), and
// freshness-based publication rules. Accepted articles are enqueued on
// the transactional outbox instead of being published directly;
// PublishOutbox (a separate use case, on its own scheduler interval) does
// the actual sending. See ADR 0005, ADR 0007, and ADR 0009.
type ProcessCycle struct {
	leagueRules     leagueRulesCache
	leagueMentions  atomic.Int64 // league mentions seen in the current cycle
	fetcher         out.Fetcher
	articles        out.ArticleRepository
	dedup           out.DeduplicationRepository
	aliases         out.AliasRepository
	similarityIndex out.SimilarityIndexRepository
	clusters        out.ClusterRepository
	decisions       out.DecisionRepository
	outbox          out.OutboxRepository
	uow             out.UnitOfWork
	freshness       publishing.FreshnessPolicy
	matcher         dictionary.Matcher
	editorialModel  *editorial.Model
	editorialVeto   *editorialveto.Config
	cascade         in.DedupCascadeConfig
	clock           out.Clock
	logger          out.Logger

	sourcePolicies       out.SourcePolicyDecisionRepository
	sourcePolicyResolver sourcepolicy.Resolver

	clusterPublications out.ClusterPublicationRepository

	seenItems out.SeenItemsRepository

	sourcePolicyCooldowns out.SourcePolicyCooldownRepository

	unknownEntities out.UnknownEntityRepository

	// entityFallback is nil when entity_extraction.fallback.enabled is
	// false — the composition root (engine/run.go) never
	// constructs one in that case, so no network call is even reachable
	// from this field's zero value; the fallback must never become a
	// dependency article processing needs.
	entityFallback *entityresolution.Deps
	lanePolicy     publishing.LivePolicy

	matchStateProvider out.MatchStateProvider // nil: no stamping, every article stays normal-lane — today's behavior

	// currentMatchState/currentMatchStateOK are set once per Run() call
	// (never fetched per-article — this bot has exactly one live game at
	// a time, so one poll per cycle is correct and avoids hammering ESPN)
	// and read by accept() for the rest of that Run.
	currentMatchState   matchstate.State
	currentMatchStateOK bool

	recentStories out.RecentStoryRepository
	titleTokens   titleTokenCache
}

// SetLivePolicy is called by the composition root. Its zero value is safe:
// all publications remain in the normal lane.
func (p *ProcessCycle) SetLivePolicy(policy publishing.LivePolicy) { p.lanePolicy = policy }

// SetMatchStateProvider is called by the composition root. Its zero
// value (nil) is safe: Run never stamps match-state metadata, and every
// article is treated exactly as it was before this feature existed.
func (p *ProcessCycle) SetMatchStateProvider(m out.MatchStateProvider) { p.matchStateProvider = m }

// SetRecentStories is called by the composition root to supply what the
// channel already carries. Its zero value (nil) is safe: the story-repeat
// guard never withholds anything.
func (p *ProcessCycle) SetRecentStories(r out.RecentStoryRepository) { p.recentStories = r }

// NewProcessCycle builds a ProcessCycle use case. editorialModel is nil
// when cascade.Editorial.Mode is "disabled" — the caller (main.go) never
// loads a model in that case; editorialVeto is nil only when
// no editorial.veto_path is configured. Deterministic vetoes are evaluated
// independently of the trained model and editorial mode.
func NewProcessCycle(
	fetcher out.Fetcher,
	articles out.ArticleRepository,
	dedup out.DeduplicationRepository,
	aliases out.AliasRepository,
	similarityIndex out.SimilarityIndexRepository,
	clusters out.ClusterRepository,
	decisions out.DecisionRepository,
	outbox out.OutboxRepository,
	uow out.UnitOfWork,
	freshness publishing.FreshnessPolicy,
	matcher dictionary.Matcher,
	editorialModel *editorial.Model,
	editorialVeto *editorialveto.Config,
	cascade in.DedupCascadeConfig,
	clock out.Clock,
	logger out.Logger,
	sourcePolicies out.SourcePolicyDecisionRepository,
	sourcePolicyResolver sourcepolicy.Resolver,
	clusterPublications out.ClusterPublicationRepository,
	seenItems out.SeenItemsRepository,
	sourcePolicyCooldowns out.SourcePolicyCooldownRepository,
	unknownEntities out.UnknownEntityRepository,
	entityFallback *entityresolution.Deps,
) *ProcessCycle {
	return &ProcessCycle{
		fetcher:         fetcher,
		articles:        articles,
		dedup:           dedup,
		aliases:         aliases,
		similarityIndex: similarityIndex,
		clusters:        clusters,
		decisions:       decisions,
		outbox:          outbox,
		uow:             uow,
		freshness:       freshness,
		matcher:         matcher,
		editorialModel:  editorialModel,
		editorialVeto:   editorialVeto,
		cascade:         cascade,
		clock:           clock,
		logger:          logger,

		sourcePolicies:       sourcePolicies,
		sourcePolicyResolver: sourcePolicyResolver,

		clusterPublications: clusterPublications,

		seenItems: seenItems,

		sourcePolicyCooldowns: sourcePolicyCooldowns,

		unknownEntities: unknownEntities,

		entityFallback: entityFallback,
	}
}

// Run implements in.ProcessCycle.
func (p *ProcessCycle) Run(ctx context.Context) (in.ProcessCycleResult, error) {
	var result in.ProcessCycleResult
	cycleID := uuid.NewString()

	p.currentMatchState, p.currentMatchStateOK = matchstate.State{}, false
	if p.matchStateProvider != nil {
		state, ok, err := p.matchStateProvider.Current(ctx, p.clock.Now())
		if err != nil {
			p.logger.Warn("match state provider failed; continuing without live-lane stamping this cycle", "error", err.Error())
		} else if ok {
			p.currentMatchState, p.currentMatchStateOK = state, true
			p.logger.Info("live match state confirmed for this cycle", "match_id", state.MatchID, "match_phase", string(state.Phase))
		}
		// ok=false with err=nil ("no live game right now") is the common,
		// expected case in the off-season and outside game windows — no
		// log needed for that.
	}

	p.leagueMentions.Store(0)
	if src := p.cascade.Cluster.LeagueEntities; src != nil && p.cascade.Cluster.Enabled {
		src.Refresh(p.clock.Now()) // one stat per cycle; fail-open inside the provider
	}

	fetched, err := p.fetcher.Fetch(ctx)
	if err != nil {
		return result, fmt.Errorf("process cycle: fetch: %w", err)
	}
	result.Fetched = len(fetched)

	for _, raw := range fetched {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := p.processOne(ctx, raw, cycleID, &result); err != nil {
			return result, err
		}
	}

	p.logger.Info(
		"process cycle completed",
		"league_mentions", p.leagueMentions.Load(),
		"fetched", result.Fetched,
		"accepted", result.Accepted,
		"rejected", result.Rejected,
		"duplicate", result.Duplicate,
		"cluster_gate_denied", result.ClusterGateDenied,
		"cluster_gate_allowed_exempt", result.ClusterGateAllowedExempt,
		"cluster_gate_allowed_update", result.ClusterGateAllowedUpdate,
		"claimed", result.Claimed,
		"seen_skipped", result.SeenSkipped,
		"revised", result.Revised,
		"in_flight_skipped", result.InFlightSkipped,
		"identity_collisions", result.IdentityCollisions,
		"claim_errors", result.ClaimErrors,
		"superseded", result.Superseded,
		"cooldown_reserved", result.CooldownReserved,
		"cooldown_blocked", result.CooldownBlocked,
		"cooldown_shadow_would_publish", result.CooldownShadowWouldPublish,
	)
	return result, nil
}

func (p *ProcessCycle) processOne(ctx context.Context, raw article.Article, cycleID string, result *in.ProcessCycleResult) error {
	now := p.clock.Now()
	a := article.Normalize(raw)
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	a.FetchedAt = now
	a.Status = article.StatusFetched

	// Everything below that persists state for this article — every
	// accept/reject/retain outcome, the Gate A/Gate B source-policy audit
	// rows, clustering (cluster/representation/event-key/decision rows),
	// aliases, the exact-fingerprint row, and article_processing_events —
	// runs inside one transaction. All of those dependent rows carry a
	// foreign key into articles(id), and SQLite checks foreign keys
	// per-statement rather than deferring to COMMIT, so this also requires
	// (below) that the article row itself is saved before any of them.
	// Wrapping the whole sequence means a failure partway through — a
	// write that violates that foreign key, or any other error — rolls
	// back every write for this article: no partial article, decision,
	// alias, fingerprint, cluster, or event is ever left behind, and a
	// retry of the same article (e.g. after a transient failure) starts
	// from a clean slate rather than duplicating audit rows (see issue
	// #152). UnitOfWork.Do is reentrant, so accept()'s own p.uow.Do call
	// later in this function joins this same transaction instead of
	// starting a second, independent one.
	return p.uow.Do(ctx, func(ctx context.Context) error {
		if err := article.Validate(a); err != nil {
			result.Rejected++
			return p.reject(ctx, a, ingestidentity.Identity{}, "", cycleID, now, article.StatusRejectedValidation, "validation", err.Error())
		}

		// Step 1: URL and text normalization (ADR 0009). article.Normalize
		// only trims whitespace; the real canonical form is computed here
		// so the domain package stays free of a config-shaped dependency.
		normalizedURL, err := normalization.NormalizeURL(a.CanonicalURL, p.cascade.Normalization.URL, p.cascade.Normalization.Resolvers)
		if err != nil {
			result.Rejected++
			return p.reject(ctx, a, ingestidentity.Identity{}, "", cycleID, now, article.StatusRejectedValidation, "validation", fmt.Sprintf("normalize url: %s", err))
		}
		a.CanonicalURL = normalizedURL

		// Step 1.5: stable ingestion identity. Resolved and
		// claimed before any row is written, so an already-terminal item
		// costs nothing beyond this claim check — no dictionary,
		// classifier, dedup, clustering, or publish work, and no new
		// articles/article_processing_events row.
		// Validation failures (above) are not covered: a canonical URL is
		// a precondition for Resolve, by design.
		identity := ingestidentity.Resolve(string(a.Source.Type), a.Source.ID.String(), a.ExternalID, a.CanonicalURL)
		contentHash := ingestidentity.ContentHash(a.CanonicalURL, a.Title, a.Summary)
		claimID := a.ID
		claim, err := p.seenItems.Claim(ctx, identity, claimID, contentHash, a.CanonicalURL, now, p.cascade.SeenItems.ReclaimAfter)
		if err != nil {
			result.ClaimErrors++
			result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.ClaimErrors++ })
			return fmt.Errorf("process cycle: claim ingest identity for %s: %w", a.CanonicalURL, err)
		}

		switch claim.Status {
		case out.SeenClaimSeen:
			result.SeenSkipped++
			result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.SeenSkipped++ })
			p.logger.Debug("seen item skipped: identical content already has a terminal outcome",
				"platform", identity.Platform, "source", identity.SourceID, "article_id", claim.ArticleID, "outcome", claim.TerminalOutcome)
			return nil
		case out.SeenClaimInFlight:
			result.InFlightSkipped++
			p.logger.Debug("seen item skipped: claimed by another in-flight worker",
				"platform", identity.Platform, "source", identity.SourceID, "article_id", claim.ArticleID)
			return nil
		case out.SeenClaimRevised:
			if identity.Basis == ingestidentity.BasisRemoteID && ingestidentity.IsCollision(identity.Basis, claim.PreviousCanonicalURL, a.CanonicalURL) {
				result.IdentityCollisions++
				result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.IdentityCollisions++ })
				p.logger.Warn("identity collision detected: disambiguating by canonical url",
					"platform", identity.Platform, "source", identity.SourceID,
					"previous_canonical_url", claim.PreviousCanonicalURL, "canonical_url", a.CanonicalURL)
				identity = ingestidentity.Resolve(string(a.Source.Type), a.Source.ID.String(), "", a.CanonicalURL)
				claim, err = p.seenItems.Claim(ctx, identity, uuid.NewString(), contentHash, a.CanonicalURL, now, p.cascade.SeenItems.ReclaimAfter)
				if err != nil {
					result.ClaimErrors++
					return fmt.Errorf("process cycle: claim disambiguated ingest identity for %s: %w", a.CanonicalURL, err)
				}
				if claim.Status == out.SeenClaimSeen {
					result.SeenSkipped++
					return nil
				}
			} else if !article.IsReprocessable(article.Status(claim.TerminalOutcome)) {
				result.Revised++
				result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.Revised++ })
				if err := p.seenItems.Finalize(ctx, identity, claim.TerminalOutcome, contentHash, a.CanonicalURL, now); err != nil {
					return fmt.Errorf("process cycle: finalize revised-but-committed seen item for %s: %w", a.CanonicalURL, err)
				}
				return nil
			}
			a.ID = claim.ArticleID
			result.Claimed++
			result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.Claimed++ })
		default: // out.SeenClaimNew
			a.ID = claim.ArticleID
			result.Claimed++
			result.BumpPlatform(string(a.Source.Type), func(pm *in.PlatformIngestMetrics) { pm.Claimed++ })
		}

		text := normalization.Normalize(joinFields(a, p.cascade.Dedup.FieldsUsed), p.cascade.Normalization.Text)
		unconditionalSource := p.cascade.Editorial.UnconditionalPublishSourceIDs[a.Source.ID.String()]
		// modelBypassSource scopes ONLY the editorial veto/model behavior it
		// was created for. It — and unconditionalSource — must
		// never disable exact dedup, near-duplicate detection, clustering,
		// source-policy retention, cooldowns or the cluster publication gate.
		modelBypassSource := p.cascade.Editorial.ModelBypassSourceIDs[a.Source.ID.String()] || unconditionalSource
		a.Status = article.StatusNormalized

		// Parent-first: persist the article row now, before
		// any later write in this transaction can reference it by foreign
		// key — in particular Gate A's shadow-mode "would retain" preview
		// a few steps down, which used to run before the article's first
		// Save (Step 6, originally) and so could try to insert a
		// source_policy_decisions row for an article that did not exist
		// yet. Save is an idempotent upsert (ON CONFLICT DO UPDATE), so
		// every later Save call in this transaction (Step 6's
		// cluster-candidate status, accept/reject/retain's terminal
		// status) safely updates this same row instead of re-inserting it.
		if err := p.articles.Save(ctx, a); err != nil {
			return fmt.Errorf("process cycle: save normalized article %s: %w", a.ID, err)
		}

		// Step 2: exact deduplication cascade — canonical-URL fingerprint,
		// then the four alias signals, in order. The first match rejects.
		fingerprint := deduplication.Fingerprint(a)
		exists, err := p.dedup.Exists(ctx, fingerprint)
		if err != nil {
			return fmt.Errorf("process cycle: check duplicate for %s: %w", a.ID, err)
		}
		if exists {
			result.Duplicate++
			return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedDuplicate, "dedup", "exact duplicate: canonical url fingerprint")
		}
		if originFP, ok := p.originFingerprint(a); ok {
			originExists, err := p.dedup.Exists(ctx, originFP)
			if err != nil {
				return fmt.Errorf("process cycle: check origin url duplicate for %s: %w", a.ID, err)
			}
			if originExists {
				result.Duplicate++
				return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedDuplicate, "dedup", "exact duplicate: aggregator origin url matches an already-processed article")
			}
		}
		matched, detail, err := p.matchAlias(ctx, a, text)
		if err != nil {
			return err
		}
		if matched {
			result.Duplicate++
			return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedDuplicate, "dedup", detail)
		}

		// Step 3: relevance classification. Gated by
		// Classification.Enabled — while disabled, ProcessCycle keeps the
		// phase 1-3 placeholder behavior exactly (never gating). ClassifierVersion
		// starts as "placeholder-v0" here — that column FKs into model_registry,
		// the editorial ML model (see Step 7), not the dictionary-based
		// classifier this phase adds — and is overwritten with the loaded
		// editorial.Model's own Version in Step 7 when one is loaded.
		a.ClassifierVersion = "placeholder-v0"
		a.DictionaryVersion = "placeholder-v0"
		var classificationDetail string
		if p.cascade.Classification.Enabled {
			titleText := normalization.Normalize(a.Title, p.cascade.Normalization.Text).ForClassification
			bodyText := normalization.Normalize(a.Summary, p.cascade.Normalization.Text).ForClassification

			var verdict classification.Result
			if p.cascade.Classification.TrustedSourceIDs[a.Source.ID.String()] {
				verdict = classification.Result{
					Related: true, Score: 100, RuleApplied: classification.RuleTrustedBypass,
					DictionaryVersion: p.matcher.Version(),
				}
			} else if p.cascade.Classification.ContextSourceIDs[a.Source.ID.String()] {
				verdict = classification.DecideWithSourceContext(titleText, bodyText, p.matcher)
			} else {
				verdict = classification.Decide(titleText, bodyText, p.matcher)
			}
			if !p.cascade.Classification.TrustedSourceIDs[a.Source.ID.String()] {
				verdict = classification.ApplyAnchor(verdict, p.cascade.Classification.Anchor, now)
			}
			a.DictionaryVersion = verdict.DictionaryVersion
			a.RelevanceScore = verdict.Score

			p.logger.Debug("classification decided", "article_id", a.ID, "related", verdict.Related, "score", verdict.Score,
				"rule", verdict.RuleApplied, "entities", verdict.EntitiesDetected)

			if !verdict.Related {
				result.Rejected++
				return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedNotRelated, "classification", explainVerdict(verdict))
			}
			classificationDetail = explainAcceptedVerdict(verdict)
		}

		// Deterministic editorial vetoes are an online input filter. They are
		// deliberately independent of editorial.mode and the trained model:
		// confirmed animal/promotional/off-topic noise must not consume
		// clustering, cooldown reservations, or outbox capacity. The event is
		// terminal and carries the category-specific reason code.
		var vetoResult editorialveto.Result
		if p.editorialVeto != nil && !modelBypassSource {
			vetoResult = editorialveto.Evaluate(a.Title+" "+a.Summary+" "+a.Body, *p.editorialVeto)
			if vetoResult.Triggered {
				result.Rejected++
				return p.rejectEditorialVeto(ctx, a, identity, contentHash, cycleID, now, vetoResult)
			}
		}

		// Step 4: candidate generation + scoring, computed once and reused by
		// both the near-duplicate decision (step 5) and the clustering
		// decision (step 6).
		candidates, scores, signals, err := p.scoreCandidates(ctx, a, text, now)
		if err != nil {
			return fmt.Errorf("process cycle: score candidates for %s: %w", a.ID, err)
		}

		// Step 5: near-duplicate decision. dedupDecision is computed
		// unconditionally (it is pure and cheap) so Step 6's clustering can
		// reuse its winning match as a dedup hint (ADR 0009: "one shared
		// candidate/score computation") even when Dedup.Enabled is false. Only
		// the reject-as-near-dup decision itself, and the content-type-exception
		// relabeling, are gated by Dedup.Enabled — candidate generation above
		// still ran unconditionally so the index stays fresh for when this is
		// re-enabled. Exact-duplicate detection (step 2) is unaffected.
		thresholds := deduplication.ResolveThresholds(p.thresholdConfig(), string(a.Source.Type), len(text.Tokens))
		dedupDecision := deduplication.Decide(toDedupCandidates(candidates), scores, signals, thresholds)
		if p.cascade.Dedup.Enabled {
			if dedupDecision.Type == deduplication.DuplicateTypeNear && p.cascade.Dedup.ContentTypeExceptions[a.ContentType] {
				// content_type_exceptions (e.g. video): near-duplicate scoring on
				// very short/generic titles is unreliable, so these content types
				// are never auto-rejected as near-duplicates — at most treated as
				// the same event, still eligible for clustering below.
				dedupDecision.Type = deduplication.DuplicateTypeSameEvent
				dedupDecision.IsDuplicate = false
			}
			if dedupDecision.Type == deduplication.DuplicateTypeNear {
				// Gate A: a source-configured policy
				// may retain a near-duplicate instead of auto-rejecting it, but
				// only when the resolver is actually in ModeEnforce — in
				// ModeShadow/ModeDisabled (or for any source with no configured
				// policy, where Resolve returns sourcepolicy.DefaultPolicy) this
				// must behave byte-identically to pre-Phase-12: reject.
				policy := p.sourcePolicyResolver.Resolve(a.Source.ID.String())
				mode := p.sourcePolicyResolver.Mode()
				retain := mode == sourcepolicy.ModeEnforce && policy.RetainNearDuplicates
				if !retain {
					// Shadow-mode "what if" preview: only when shadow mode is
					// exactly what suppressed retention (enforce mode would
					// have retained this near-duplicate) is there anything
					// policy-relevant to report — ModeDisabled and sources
					// with no policy (RetainNearDuplicates false) have nothing
					// non-default to say, matching Gate B's existing
					// CodeStandard-is-not-recorded convention. Clustering has
					// not run yet here, so the eventual MemberRole, Novelty,
					// and publish outcome are genuinely unknown — do not
					// overclaim them.
					if mode == sourcepolicy.ModeShadow && policy.RetainNearDuplicates {
						if err := p.sourcePolicies.Save(ctx, out.SourcePolicyDecisionRecord{
							ID:                   uuid.NewString(),
							ArticleID:            a.ID,
							SourceID:             a.Source.ID.String(),
							Authority:            string(policy.Authority),
							PolicyVersion:        policy.Version,
							ClusterID:            "",
							DedupMatchType:       string(dedupDecision.Type),
							MemberRole:           "",
							Novelty:              "",
							DecisionCode:         string(sourcepolicy.CodeWouldRetainNearDuplicate),
							WouldPublishStandard: false,
							WouldPublishSpecial:  false,
							Mode:                 string(sourcepolicy.ModeShadow),
							Explanation:          "policy would retain this near-duplicate for clustering under enforce mode; shadow mode kept the standard reject behavior; the eventual publish outcome is unknown since clustering did not run",
							DecidedAt:            now,
						}); err != nil {
							return fmt.Errorf("process cycle: record shadow-mode source policy decision for %s: %w", a.ID, err)
						}
					}
					p.logger.Info("dedup trace: near-duplicate rejected", "article_id", a.ID, "source", a.Source.ID.String(),
						"canonical_url", a.CanonicalURL, "best_candidate_id", dedupDecision.MatchedArticleID,
						"similarity", dedupDecision.Score, "threshold", dedupDecision.Threshold,
						"model_bypass", modelBypassSource, "unconditional", unconditionalSource)
					result.Duplicate++
					return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedDuplicate, "dedup", dedupDecision.Explanation)
				}
				// Policy allows retention: fall through to clustering (Step 6)
				// instead of rejecting.
			}
		}

		// Step 6: event clustering (ADR 0009). The article row is saved first
		// under StatusClusterCandidate (its foreign-key-referencing rows require
		// it to exist), then its dedup terms are indexed — after candidate
		// generation already ran, so this article never matches itself — then
		// clustering runs, all unconditionally (not undone if freshness later
		// rejects, matching Phase 2's existing clustering behavior).
		a.Status = article.StatusClusterCandidate
		if err := p.articles.Save(ctx, a); err != nil {
			return fmt.Errorf("process cycle: save cluster candidate %s: %w", a.ID, err)
		}
		if err := p.similarityIndex.IndexTerms(ctx, a.ID, text.Shingles); err != nil {
			return fmt.Errorf("process cycle: index terms for %s: %w", a.ID, err)
		}
		clusterDecision, err := p.cluster(ctx, &a, dedupDecision, cycleID, now)
		if err != nil {
			return err
		}
		a.Status = article.StatusClustered
		if clusterDecision.Action == clustering.ActionAmbiguous && !p.cascade.PublishAmbiguousClusters {
			result.Rejected++
			return p.retainClusterAmbiguous(ctx, a, identity, contentHash, cycleID, now, clusterDecision)
		}

		// Gate B (cooldown reservation):
		// now that clustering has assigned the real MemberRole, ask source
		// policy whether this specific member should still publish. Always
		// evaluated (even in shadow/disabled mode) so the audit trail is
		// complete; only the final (post-reservation) decision actually
		// changes control flow, and only when the resolver is in enforce
		// mode.
		policy := p.sourcePolicyResolver.Resolve(a.Source.ID.String())
		memberRole := clustering.MemberRole(a.ClusterMemberRole)
		mode := p.sourcePolicyResolver.Mode()
		var cooldownRemaining time.Duration
		if a.ClusterID != "" && policy.EventCooldown > 0 {
			cooldownUntil, ok, err := p.sourcePolicyCooldowns.ActiveCooldownUntil(ctx, a.Source.ID.String(), a.ClusterID)
			if err != nil {
				return fmt.Errorf("process cycle: lookup source policy cooldown for %s: %w", a.ID, err)
			}
			if ok {
				if remaining := cooldownUntil.Sub(now); remaining > 0 {
					cooldownRemaining = remaining
				}
			}
		}
		policyDecision := sourcepolicy.Decide(memberRole, policy, cooldownRemaining, now)
		// wouldPublishSpecial is captured here, immediately after Decide
		// returns, and is NEVER reassigned below — this is the pure,
		// unconditional Decide output the design doc requires
		// would_publish_special to stay (audit/shadow-analysis telemetry,
		// never touched by ResolveCooldownReservation's possible override).
		// policyDecision itself IS reassigned below when the reservation is
		// lost to a race (OutcomeBlocked) — do not use policyDecision.
		// ShouldPublish for the audit row's WouldPublishSpecial field, use
		// this variable.
		wouldPublishSpecial := policyDecision.ShouldPublish

		var reservationOutcome sourcepolicy.ReservationOutcome
		// Gated on policy.EventCooldown > 0 (matching the read-side guard
		// above), and this scope encloses the ResolveCooldownReservation
		// call too, not just the Reserve/ForceReserve attempt: acquired
		// defaults to false, and ResolveCooldownReservation cannot tell
		// "no cooldown configured, nothing attempted" apart from "CAS
		// attempt lost the race" — passing acquired=false for a policy
		// with no cooldown at all would wrongly synthesize
		// OutcomeBlocked/CodeRejectCooldown for every enforce-mode
		// confirmation/substantial-update from a source with
		// EventCooldown unset.
		if a.ClusterID != "" && policy.EventCooldown > 0 {
			reservation := out.SourcePolicyCooldownReservation{
				SourceID: a.Source.ID.String(), ClusterID: a.ClusterID, ArticleID: a.ID,
				DecisionCode: string(policyDecision.Code), PolicyVersion: policy.Version,
			}
			acquired := false
			cooldownRelevant := mode == sourcepolicy.ModeEnforce && policyDecision.ShouldPublish &&
				sourcepolicy.IsCooldownRelevant(policyDecision.Code)
			switch {
			case cooldownRelevant && memberRole == clustering.MemberRoleCorrection:
				if err := p.sourcePolicyCooldowns.ForceReserve(ctx, reservation, policy.EventCooldown, now); err != nil {
					return fmt.Errorf("process cycle: force-reserve source policy cooldown for %s: %w", a.ID, err)
				}
				acquired = true
			case cooldownRelevant:
				var err error
				acquired, err = p.sourcePolicyCooldowns.Reserve(ctx, reservation, policy.EventCooldown, now)
				if err != nil {
					return fmt.Errorf("process cycle: reserve source policy cooldown for %s: %w", a.ID, err)
				}
			}
			policyDecision, reservationOutcome = sourcepolicy.ResolveCooldownReservation(policyDecision, memberRole, mode, acquired)
		}

		switch reservationOutcome {
		case sourcepolicy.OutcomeReserved, sourcepolicy.OutcomeForced:
			result.CooldownReserved++
		case sourcepolicy.OutcomeShadow:
			if policyDecision.ShouldPublish {
				result.CooldownShadowWouldPublish++
			}
		}
		if policyDecision.Code == sourcepolicy.CodeRejectCooldown {
			result.CooldownBlocked++
		}

		// Only persist an audit row when a non-default policy actually had
		// something to say — CodeStandard is what every ordinary article gets
		// (no policy configured, or a role policy has no opinion about), and
		// recording one there would flood source_policy_decisions with a row
		// for every single article ever processed.
		if policyDecision.Code != sourcepolicy.CodeStandard {
			if err := p.recordSourcePolicyDecision(ctx, a, dedupDecision, policyDecision, wouldPublishSpecial, reservationOutcome, now); err != nil {
				return err
			}
		}

		if mode == sourcepolicy.ModeEnforce && !policyDecision.ShouldPublish && policyDecision.Code != sourcepolicy.CodeStandard {
			result.Rejected++
			return p.retainNoPublish(ctx, a, identity, contentHash, cycleID, now, policyDecision)
		}

		// Step 7: editorial-quality classification. editorialModel
		// is nil when Editorial.Mode == "disabled" — editorial.Evaluate
		// reproduces the exact pre-Phase-7 placeholder behavior for that path.
		isPrimarySource := p.cascade.Classification.TrustedSourceIDs[a.Source.ID.String()]
		a.IsTrustedSource = isPrimarySource
		editorialScore := editorial.Evaluate(a, isPrimarySource, p.editorialModel)
		a.EditorialScore = editorialScore.Value
		if p.editorialModel != nil {
			a.ClassifierVersion = editorialScore.ModelVersion
			a.QualityThreshold = p.editorialModel.Thresholds.Accept
		}

		switch {
		case p.cascade.Editorial.Mode == "shadow" && p.editorialModel != nil:
			if err := p.appendEditorialEvent(ctx, a, cycleID, now, "editorial_shadow", editorialScore); err != nil {
				return err
			}
		case p.cascade.Editorial.Mode == "enforce" && editorialScore.Zone == editorial.ZoneReject && !modelBypassSource:
			result.Rejected++
			return p.rejectEditorial(ctx, a, identity, contentHash, cycleID, now, editorialScore)
		case p.cascade.Editorial.Mode == "enforce" && editorialScore.Zone == editorial.ZoneAmbiguous && p.cascade.Editorial.AmbiguousPolicy == "reject" && !modelBypassSource:
			result.Rejected++
			return p.rejectEditorialAmbiguous(ctx, a, identity, contentHash, cycleID, now, editorialScore)
		case p.cascade.Editorial.Mode == "enforce" && editorialScore.Zone == editorial.ZoneAmbiguous:
			// AmbiguousPolicy == "accept": explicit, configured fall-through.
			if err := p.appendEditorialEvent(ctx, a, cycleID, now, "editorial_ambiguous", editorialScore); err != nil {
				return err
			}
		case p.cascade.Editorial.Mode == "canary" && editorialScore.Zone == editorial.ZoneReject && CanaryEnforced(a.ID, p.cascade.Editorial.CanaryPercent) && !modelBypassSource:
			result.Rejected++
			return p.rejectEditorialScored(ctx, a, identity, contentHash, cycleID, now, editorialScore, "editorial:canary_reject")
		case p.cascade.Editorial.Mode == "canary" && editorialScore.Zone == editorial.ZoneAmbiguous && p.cascade.Editorial.AmbiguousPolicy == "reject" && CanaryEnforced(a.ID, p.cascade.Editorial.CanaryPercent) && !modelBypassSource:
			result.Rejected++
			return p.rejectEditorialScored(ctx, a, identity, contentHash, cycleID, now, editorialScore, "editorial:canary_ambiguous_reject")
		case p.cascade.Editorial.Mode == "canary":
			// Shadow arm of canary (not selected for enforcement), or an
			// enforced-arm article that scored accept: log-only, same
			// shape as plain shadow mode, but distinguishable by
			// reason/event naming for canary reporting (Task 11).
			eventType := "editorial_canary_shadow"
			if editorialScore.Zone == editorial.ZoneReject || (editorialScore.Zone == editorial.ZoneAmbiguous && p.cascade.Editorial.AmbiguousPolicy == "reject") {
				eventType = "editorial_canary_shadow_would_reject"
			}
			if err := p.appendEditorialEvent(ctx, a, cycleID, now, eventType, editorialScore); err != nil {
				return err
			}
		}

		// Step 7.5: story-repeat guard. Withholds an article when the channel
		// already carries Cascade.StoryGuard.MaxPerStory messages about the
		// same story. Official and model-bypass sources are exempt.
		if repeated, matched, err := p.storyRepeated(ctx, a, text, now); err != nil {
			return err
		} else if repeated && !modelBypassSource && !unconditionalSource {
			if a.ClusterID != "" {
				if _, err := p.sourcePolicyCooldowns.Release(ctx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
					return fmt.Errorf("process cycle: release source policy cooldown for %s: %w", a.ID, err)
				}
			}
			result.Rejected++
			return p.retainStoryRepeat(ctx, a, identity, contentHash, cycleID, now, matched)
		}

		// Step 8: freshness gate, then the cluster publication gate (at most one initial publication per cluster,
		// enforced in persistence, independent of sourcepolicy.Mode), then
		// accept or reject.
		a.FreshnessMaxAge = p.freshness.MaxAgeFor(a)
		if p.gameInProgress() {
			a.FreshnessMaxAge = p.freshness.MaxAgeDuringLiveGame(a)
		}
		if unconditionalSource && !a.PublishedAt.IsZero() {
			a.FreshnessMaxAge = max(a.FreshnessMaxAge, now.Sub(a.PublishedAt)+24*time.Hour)
		}
		if !unconditionalSource && !publishing.DecideStored(a, now) {
			if a.ClusterID != "" {
				if _, err := p.sourcePolicyCooldowns.Release(ctx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
					return fmt.Errorf("process cycle: release source policy cooldown for %s: %w", a.ID, err)
				}
			}
			result.Rejected++
			return p.reject(ctx, a, identity, contentHash, cycleID, now, article.StatusRejectedStale, "freshness", "published_at outside freshness window")
		}

		gateOutcome := clustergate.Outcome("not_evaluated")
		if a.ClusterID != "" {
			outcome, err := p.evaluateClusterGate(ctx, a, memberRole, policyDecision, now)
			if err != nil {
				return err
			}
			gateOutcome = outcome
			switch outcome {
			case clustergate.OutcomeDeniedDuplicate:
				result.Rejected++
				result.ClusterGateDenied++
				return p.denyClusterDuplicate(ctx, a, identity, contentHash, cycleID, now, memberRole, policyDecision)
			case clustergate.OutcomeAllowedExempt:
				result.ClusterGateAllowedExempt++
			case clustergate.OutcomeAllowedUpdate:
				result.ClusterGateAllowedUpdate++
			}
		}

		// Dedup/cluster trace: one line explaining why this
		// candidate was not blocked, including any bypass flag in effect
		// (which only ever affects editorial scoring now).
		p.logger.Info("dedup trace: candidate allowed", "article_id", a.ID, "source", a.Source.ID.String(),
			"canonical_url", a.CanonicalURL, "ingest_identity", identity.Key, "fingerprint", fingerprint,
			"best_candidate_id", dedupDecision.MatchedArticleID, "similarity", dedupDecision.Score, "threshold", dedupDecision.Threshold,
			"dedup_type", string(dedupDecision.Type), "cluster_id", a.ClusterID, "member_role", string(memberRole),
			"policy_decision", string(policyDecision.Code), "cluster_gate", string(gateOutcome),
			"model_bypass", modelBypassSource, "unconditional", unconditionalSource)

		return p.accept(ctx, a, identity, contentHash, fingerprint, text, cycleID, now, result, classificationDetail)
	})
}

// evaluateClusterGate attempts to reserve a.ClusterID's initial-publication
// slot — always attempted first, unconditionally, regardless of
// role — and returns the resulting clustergate.Outcome. See
// clustergate.Decide's doc comment for the full decision rule.
func (p *ProcessCycle) evaluateClusterGate(ctx context.Context, a article.Article, role clustering.MemberRole, policyDecision sourcepolicy.Decision, now time.Time) (clustergate.Outcome, error) {
	acquired, err := p.clusterPublications.Reserve(ctx, out.ClusterPublicationReservation{
		ClusterID: a.ClusterID, ArticleID: a.ID, SourceID: a.Source.ID.String(),
		MemberRole: string(role), DecisionCode: string(policyDecision.Code),
		PolicyVersion: p.sourcePolicyResolver.Resolve(a.Source.ID.String()).Version,
		Reason:        fmt.Sprintf("role %s, source-policy decision %s", role, policyDecision.Code),
	}, now)
	if err != nil {
		return "", fmt.Errorf("process cycle: reserve cluster publication gate for %s: %w", a.ID, err)
	}
	outcome := clustergate.Decide(acquired, role, policyDecision.Code)
	// An official confirmation is only meaningful as a first official voice on
	// a story. When an official-authority source already has this cluster
	// queued or published, a further official confirmation from another source (the same
	// content cross-posted to another platform) adds no new information:
	// platform difference alone is not novelty. Substantial updates and
	// corrections are unaffected.
	if outcome == clustergate.OutcomeAllowedUpdate && policyDecision.Code == sourcepolicy.CodePublishOfficialConfirmation {
		if reader, ok := p.clusterPublications.(out.ClusterPublishedSourceReader); ok {
			sources, err := reader.PublishedSourceIDs(ctx, a.ClusterID, a.ID)
			if err != nil {
				return "", fmt.Errorf("process cycle: list published sources for cluster %s: %w", a.ClusterID, err)
			}
			for _, sid := range sources {
				// Same-source repeats stay governed by the event cooldown; this
				// only catches another official platform mirroring the story.
				if sid != a.Source.ID.String() && isOfficialAuthority(p.sourcePolicyResolver.Resolve(sid).Authority) {
					return clustergate.OutcomeDeniedDuplicate, nil
				}
			}
		}
	}
	return outcome, nil
}

// denyClusterDuplicate saves a as StatusRetainedNoPublish because the
// cluster publication gate already holds an active reservation
// for a.ClusterID and this article is neither an exempt role nor an
// explicitly policy-authorized material update. Mirrors retainNoPublish's
// shape, but the reason comes from the cluster gate, not sourcepolicy.
// Decide's own ShouldPublish.
func (p *ProcessCycle) denyClusterDuplicate(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, role clustering.MemberRole, policyDecision sourcepolicy.Decision) error {
	fromStatus := a.Status
	reason := fmt.Sprintf("cluster %s already has an active initial publication; role %s with decision %s is not an authorized material update", a.ClusterID, role, policyDecision.Code)
	a.Status = article.StatusRetainedNoPublish
	a.StatusReason = "cluster_gate_denied_duplicate"
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save cluster-gate-denied article %s: %w", a.ID, err)
	}
	if a.ClusterID != "" {
		if _, err := p.sourcePolicyCooldowns.Release(ctx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
			return fmt.Errorf("process cycle: release source policy cooldown for %s: %w", a.ID, err)
		}
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: a.Status,
		EventType: "cluster_gate_denied_duplicate", ReasonCode: "cluster_gate_denied_duplicate", ReasonDetail: reason, CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article denied by cluster publication gate", "cycle_id", cycleID, "article_id", a.ID,
		"cluster_id", a.ClusterID, "source", a.Source.ID, "role", string(role), "policy_decision", string(policyDecision.Code))
	return nil
}

// matchAlias checks the alias-based exact-duplicate signals (external ID,
// original-URL alias, content fingerprint, and — when no external ID is
// available — the attribute-combination signal), in that order, returning
// a human-readable reject detail for the first match.
func (p *ProcessCycle) matchAlias(ctx context.Context, a article.Article, text normalization.Representations) (bool, string, error) {
	type check struct {
		aliasType out.AliasType
		value     string
	}
	var checks []check
	if a.ExternalID != "" && a.Source.ID != (source.ID{}) {
		checks = append(checks, check{out.AliasTypeExternalID, a.Source.ID.String() + ":" + a.ExternalID})
	}
	if a.OriginalURL != "" {
		checks = append(checks, check{out.AliasTypeURL, deduplication.URLAlias(a.OriginalURL)})
	}
	checks = append(checks, check{out.AliasTypeContentFingerprint, deduplication.ContentFingerprint(text.ForDedup)})
	if a.ExternalID == "" {
		combo := deduplication.AttributeCombo(a.Source.ID.String(), text.ForDedup, a.PublishedAt, p.cascade.Dedup.AttributeComboWindow)
		checks = append(checks, check{out.AliasTypeAttributeCombo, combo})
	}

	for _, c := range checks {
		id, err := p.aliases.FindArticleID(ctx, c.aliasType, c.value)
		if err == nil {
			return true, fmt.Sprintf("exact duplicate via %s alias, matches article %s", c.aliasType, id), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, "", fmt.Errorf("process cycle: check alias %s for %s: %w", c.aliasType, a.ID, err)
		}
	}
	return false, "", nil
}

// originMetadataKey is the article.Metadata key adapters set when an
// aggregator/feed exposes the original article URL behind its wrapper link.
// truncatedMinShingles is the minimum evidence (distinct shingles) a truncated
// headline needs before containment scoring may call it a duplicate.
const truncatedMinShingles = 4

const originMetadataKey = "origin_url"

// originFingerprint returns the exact-identity fingerprint of the original
// URL an aggregator item points at, when the adapter exposed one.
func (p *ProcessCycle) originFingerprint(a article.Article) (string, bool) {
	raw := a.Metadata[originMetadataKey]
	if raw == "" {
		return "", false
	}
	normalized, err := normalization.NormalizeURL(raw, p.cascade.Normalization.URL, p.cascade.Normalization.Resolvers)
	if err != nil || normalized == "" || normalized == a.CanonicalURL {
		return "", false
	}
	return deduplication.Fingerprint(article.Article{CanonicalURL: normalized}), true
}

// saveAliases records the same alias checks matchAlias performed, so a
// future article can match against this one.
func (p *ProcessCycle) saveAliases(ctx context.Context, a article.Article, text normalization.Representations) error {
	if a.ExternalID != "" && a.Source.ID != (source.ID{}) {
		if err := p.aliases.Save(ctx, out.AliasTypeExternalID, a.Source.ID.String()+":"+a.ExternalID, a.ID); err != nil {
			return fmt.Errorf("save external_id alias: %w", err)
		}
	}
	if a.OriginalURL != "" {
		if err := p.aliases.Save(ctx, out.AliasTypeURL, deduplication.URLAlias(a.OriginalURL), a.ID); err != nil {
			return fmt.Errorf("save url alias: %w", err)
		}
	}
	if err := p.aliases.Save(ctx, out.AliasTypeContentFingerprint, deduplication.ContentFingerprint(text.ForDedup), a.ID); err != nil {
		return fmt.Errorf("save content_fingerprint alias: %w", err)
	}
	if a.ExternalID == "" {
		combo := deduplication.AttributeCombo(a.Source.ID.String(), text.ForDedup, a.PublishedAt, p.cascade.Dedup.AttributeComboWindow)
		if err := p.aliases.Save(ctx, out.AliasTypeAttributeCombo, combo, a.ID); err != nil {
			return fmt.Errorf("save attribute_combo alias: %w", err)
		}
	}
	return nil
}

// scoreCandidates finds candidates via the SQL-filtered inverted index and
// scores each one against the incoming article's shingles, once, for reuse
// by both the near-duplicate and clustering decisions.
func (p *ProcessCycle) scoreCandidates(ctx context.Context, a article.Article, text normalization.Representations, now time.Time) ([]out.Candidate, map[string]float64, map[string][]string, error) {
	window := time.Duration(p.cascade.Dedup.WindowHours) * time.Hour
	candidates, err := p.similarityIndex.FindCandidates(ctx, out.CandidateFilter{
		WindowStart:     now.Add(-window),
		ContentType:     a.ContentType,
		ExcludeStatuses: []article.Status{article.StatusRejectedValidation, article.StatusRejectedDuplicate},
		Terms:           text.Shingles,
		MinSharedTerms:  p.cascade.Dedup.MinSharedTerms,
		Limit:           p.cascade.Dedup.MaxCandidates,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("find candidates: %w", err)
	}

	scores := make(map[string]float64, len(candidates))
	signals := make(map[string][]string, len(candidates))
	for _, c := range candidates {
		// The index only stores terms, not full Representations, so the
		// candidate's shingles are re-derived from its stored title using
		// the same text config used for the incoming article.
		candidateText := normalization.Normalize(c.Title, p.cascade.Normalization.Text)
		score := p.score(text.Shingles, candidateText.Shingles)
		// A social post that cut its headline with an ellipsis is a prefix of
		// the full headline, so Jaccard understates the match. When the
		// truncated side still carries enough evidence, score by how fully
		// the full text contains it. Both sides truncated: no containment
		// claim either way.
		if text.Truncated != candidateText.Truncated {
			short, long := text.Shingles, candidateText.Shingles
			if candidateText.Truncated {
				short, long = long, short
			}
			if len(short) >= truncatedMinShingles {
				score = max(score, similarity.Containment(short, long))
			}
		}
		scores[c.ArticleID] = score
		signals[c.ArticleID] = []string{
			fmt.Sprintf("%s=%.2f", p.cascade.Dedup.Method, score),
			fmt.Sprintf("shared_terms=%d", sharedTermCount(text.Shingles, candidateText.Shingles)),
		}
	}
	return candidates, scores, signals, nil
}

func (p *ProcessCycle) score(a, b []string) float64 {
	if p.cascade.Dedup.Method == "dice" {
		return similarity.Dice(a, b)
	}
	return similarity.Jaccard(a, b)
}

func (p *ProcessCycle) thresholdConfig() deduplication.ThresholdConfig {
	return deduplication.ThresholdConfig{
		DefaultThreshold:      p.cascade.Dedup.DefaultThreshold,
		ThresholdBySourceType: p.cascade.Dedup.ThresholdBySourceType,
		ShortTextMaxTokens:    p.cascade.Dedup.ShortTextMaxTokens,
		ShortTextThreshold:    p.cascade.Dedup.ShortTextThreshold,
		EventThreshold:        p.cascade.Dedup.EventThreshold,
		RulesVersion:          p.cascade.Dedup.RulesVersion,
	}
}

// explainVerdict renders a classification.Result as a single human-readable
// line for article_processing_events.reason_detail, the same pattern
// deduplication.Decision.Explanation already established — no JSON blob,
// just enough detail to audit the decision without a join.
func explainVerdict(v classification.Result) string {
	entities := "none"
	if len(v.EntitiesDetected) > 0 {
		entities = strings.Join(v.EntitiesDetected, ", ")
	}
	return fmt.Sprintf(
		"not related: score %.2f (rule %s, dictionary %s); entities detected: %s",
		v.Score, v.RuleApplied, v.DictionaryVersion, entities,
	)
}

// explainAcceptedVerdict renders a classification.Result that decided
// Related=true as a single human-readable line for
// article_processing_events.reason_detail on the accept path — mirroring
// explainVerdict's reject-path detail, so an accepted article's relevance
// decision is auditable too, not only a rejected one.
func explainAcceptedVerdict(v classification.Result) string {
	entities := "none"
	if len(v.EntitiesDetected) > 0 {
		entities = strings.Join(v.EntitiesDetected, ", ")
	}
	return fmt.Sprintf(
		"related: score %.2f (rule %s, dictionary %s); entities detected: %s",
		v.Score, v.RuleApplied, v.DictionaryVersion, entities,
	)
}

func joinFields(a article.Article, fields []string) string {
	for _, f := range fields {
		if f == "title" {
			return a.Title
		}
	}
	return ""
}

func sharedTermCount(a, b []string) int {
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	count := 0
	seen := make(map[string]bool, len(b))
	for _, t := range b {
		if set[t] && !seen[t] {
			count++
			seen[t] = true
		}
	}
	return count
}

func toDedupCandidates(candidates []out.Candidate) []deduplication.Candidate {
	result := make([]deduplication.Candidate, len(candidates))
	for i, c := range candidates {
		result[i] = deduplication.Candidate{ArticleID: c.ArticleID, ClusterID: c.ClusterID}
	}
	return result
}

// defaultClusterCandidateWindow is the fallback clustering candidate-search
// window (see clusterCandidateWindow), used only when Cluster.Rules.
// EventWindows configures no positive window at all (e.g. Cluster.Rules is
// left zero-valued, as in a test or a not-yet-configured deployment).
// Production rules populate all 12 known eventsignals.EventTypes with a
// positive window (clusteringrules.Validate enforces this), so this
// fallback is not expected to be exercised there.
const defaultClusterCandidateWindow = 30 * 24 * time.Hour

// defaultClusterCandidateLimit bounds ClusterRepository.
// FindCandidatesByEventKeys when ClusterSettings.CandidateLimit is left
// unset (<= 0).
const defaultClusterCandidateLimit = 50

// clusterCandidateWindow returns how far back (from now) ProcessCycle
// searches for clustering candidates: the widest EventWindow configured
// across Cluster.Rules.EventWindows. clustering.DecideJoin's own conflict
// gate (see clustering.eventDateWindow) already disqualifies any pair whose
// EventDates fall outside max(incoming, candidate).EventWindow, so bounding
// the SQL-level search to that same widest window can never exclude a
// candidate DecideJoin would otherwise have considered — it only avoids
// fetching rows the domain package would immediately discard as
// out-of-window anyway. Falls back to defaultClusterCandidateWindow when no
// window is configured at all.
func (p *ProcessCycle) clusterCandidateWindow() time.Duration {
	widest := time.Duration(0)
	for _, w := range p.cascade.Cluster.Rules.EventWindows {
		if w > widest {
			widest = w
		}
	}
	if widest <= 0 {
		return defaultClusterCandidateWindow
	}
	return widest
}

func (p *ProcessCycle) clusterCandidateLimit() int {
	if p.cascade.Cluster.CandidateLimit > 0 {
		return p.cascade.Cluster.CandidateLimit
	}
	return defaultClusterCandidateLimit
}

// dedupRelationFor maps a deduplication.DuplicateType to the
// clustering.Relation it corresponds to as a caller-supplied dedup hint
// (clustering.Candidate.DuplicateRelation) — "" (no opinion) for any type
// that is not itself a copy/duplicate signal. DuplicateTypeExact is mapped
// for completeness even though deduplication.Decide never actually produces
// it (exact duplicates are caught earlier, in ProcessCycle's Step 2 alias/
// fingerprint cascade, before this article's candidates are ever scored).
func dedupRelationFor(t deduplication.DuplicateType) clustering.Relation {
	switch t {
	case deduplication.DuplicateTypeExact:
		return clustering.RelationExactDuplicate
	case deduplication.DuplicateTypeNear:
		return clustering.RelationNearCopy
	default:
		return ""
	}
}

// applyDedupHint populates DuplicateRelation/DuplicateScore on whichever
// clustering candidate (if any) is the same article as dedupDecision's
// winning match — wiring deduplication.Decide's already-computed result
// into clustering.Compare's scoring as a dedup hint, per ADR 0009's "one
// shared candidate/score computation" principle (see
// clustering.Candidate.DuplicateRelation's doc comment). If dedupDecision
// found no match, or its match is not among candidates (a different
// article, or dedupDecision.Type carries no meaningful Relation — see
// dedupRelationFor), every candidate is left with its zero-valued (no
// opinion) DuplicateRelation, and clustering.Compare scores it purely on
// event signals.
func applyDedupHint(candidates []clustering.Candidate, dedupDecision deduplication.Decision) {
	if dedupDecision.MatchedArticleID == "" {
		return
	}
	relation := dedupRelationFor(dedupDecision.Type)
	if relation == "" {
		return
	}
	for i := range candidates {
		if candidates[i].ArticleID == dedupDecision.MatchedArticleID {
			candidates[i].DuplicateRelation = relation
			candidates[i].DuplicateScore = dedupDecision.Score
		}
	}
}

// winningClusterCandidate returns, among candidates belonging to clusterID,
// whichever one clustering.Compare (scored under cfg against incoming)
// ranks highest — reconstructing clustering.DecideJoin's internal
// per-cluster winner, which clustering.Decision itself does not expose
// (only the winning Comparison's Score/Evidence survive on Decision, not
// which candidate produced them). Used only to build the "current
// representative" proxy maybeUpdateRepresentative needs — see that
// function's doc comment for why a proxy, rather than the cluster's actual
// designated representative, is used here.
func winningClusterCandidate(incoming eventsignals.Representation, candidates []clustering.Candidate, clusterID string, cfg clustering.Config) (clustering.Candidate, bool) {
	var best clustering.Candidate
	bestScore := -1.0
	found := false
	for _, c := range candidates {
		if c.ClusterID != clusterID {
			continue
		}
		cmp := clustering.Compare(incoming, c, cfg)
		if !found || cmp.Score > bestScore {
			best, bestScore, found = c, cmp.Score, true
		}
	}
	return best, found
}

// explainClusterDecision renders a clustering.Decision as a single
// human-readable line for article_processing_events.reason_detail,
// mirroring explainVerdict/explainAcceptedVerdict's pattern for
// classification.
func explainClusterDecision(d clustering.Decision, role clustering.MemberRole) string {
	switch d.Action {
	case clustering.ActionJoin:
		return fmt.Sprintf("joined cluster %s as %s: score %.2f (rules %s)", d.ClusterID, role, d.Score, d.RulesVersion)
	case clustering.ActionAmbiguous:
		return fmt.Sprintf("ambiguous: best score %.2f neither cleared nor clearly missed join_threshold (rules %s); retained without publishing for review", d.Score, d.RulesVersion)
	default: // clustering.ActionCreate
		return fmt.Sprintf("created new cluster: best score %.2f below ambiguous_low (rules %s)", d.Score, d.RulesVersion)
	}
}

// augmentRulesWithResolved returns a copy of rules whose Entities slice
// has one ephemeral eventsignals.EntityRule appended per resolved entity
// — never mutating rules.Entities' backing array, since rules is shared
// across every article ProcessCycle processes in a cycle. The ephemeral
// rules exist only for the eventsignals.Extract call this augmented
// value is passed to; nothing here is ever written to
// configs/entity_catalog_*.yml.
func augmentRulesWithResolved(rules eventsignals.Rules, resolved []entityresolution.ResolvedEntity) eventsignals.Rules {
	if len(resolved) == 0 {
		return rules
	}
	entities := make([]eventsignals.EntityRule, len(rules.Entities), len(rules.Entities)+len(resolved))
	copy(entities, rules.Entities)
	for _, re := range resolved {
		terms := []string{re.CandidateText}
		if re.CanonicalName != re.CandidateText {
			terms = append(terms, re.CanonicalName)
		}
		entities = append(entities, eventsignals.EntityRule{
			ID:            eventsignals.Slug(re.CanonicalName),
			CanonicalName: re.CanonicalName,
			Kind:          eventsignals.EntityKind(re.Type),
			Terms:         terms,
		})
	}
	rules.Entities = entities
	return rules
}

// cluster runs Step 6: event-signal-based clustering for an article already
// saved under StatusClusterCandidate. It mutates a.ClusterID and appends the
// clustering-specific audit event (event_type "clustering", separate from
// the eventual accept/reject event) to article_processing_events.
//
// Persistence order is CreateCluster/AddMember first, then
// SaveRepresentation/SaveEventKeys/SaveDecision, then (ActionJoin only) a
// possible UpdateRepresentative — not the plan's literal prose order
// ("representation, keys, decision, membership, representative"), because
// event_keys.cluster_id and event_clustering_decisions.chosen_cluster_id
// both carry a foreign key into event_clusters(id) (migration
// 0008_event_clustering.sql): the cluster row must exist before either of
// those rows can be inserted, which for ActionCreate/ActionAmbiguous is
// only true after CreateCluster runs.
//
// ActionAmbiguous is handled like ActionCreate for clustering persistence
// (a new open cluster, founding role MemberRoleFirstReport — DecideJoin
// never assigns that role itself, see its own doc comment) so operators
// keep a full audit trail. The caller then retains the article without
// publishing, because production ambiguity has proven too likely to be a
// low-value near-duplicate.
func (p *ProcessCycle) cluster(ctx context.Context, a *article.Article, dedupDecision deduplication.Decision, cycleID string, now time.Time) (clustering.Decision, error) {
	if !p.cascade.Cluster.Enabled {
		clusterID := uuid.NewString()
		if err := p.clusters.CreateCluster(ctx, clusterID, a.ID, clustering.StatusOpen, now); err != nil {
			return clustering.Decision{}, fmt.Errorf("process cycle: create cluster for %s: %w", a.ID, err)
		}
		a.ClusterID = clusterID
		a.ClusterMemberRole = string(clustering.MemberRoleFirstReport)
		decision := clustering.Decision{Action: clustering.ActionCreate, DecidedAt: now}
		return decision, p.appendClusterEvent(ctx, a.ID, cycleID, now, decision, "clustering disabled: every article gets its own cluster")
	}

	rules := p.leagueRules.rulesFor(p.cascade.Cluster.Rules, p.cascade.Cluster.LeagueEntities)
	rep := eventsignals.Extract(*a, rules, now)
	rawUnknownEntities := rep.UnknownEntities
	for _, m := range rep.EntityMentions {
		if m.Scope == eventsignals.ScopeLeague {
			p.leagueMentions.Add(1)
		}
	}

	if p.entityFallback != nil && len(rep.UnknownEntities) > 0 {
		articleText := map[string]string{"title": a.Title, "description": a.Summary, "body": a.Body}
		resolved := entityresolution.Resolve(ctx, a.ID, rep.UnknownEntities, articleText, *p.entityFallback, now)
		if len(resolved) > 0 {
			augmented := augmentRulesWithResolved(rules, resolved)
			rep = eventsignals.Extract(*a, augmented, now)

			// Audit trail for Finding 5 of the 2026-09-16 final review: a
			// successful online resolution silently changed this article's
			// event-signal representation (e.g. its PrimaryEntity), and
			// without a log line here an operator has no way to notice —
			// and correct, per the design spec's own risk section — a bad
			// resolution after the fact.
			p.logger.Info("entity resolution fallback changed representation", "article_id", a.ID, "resolved_count", len(resolved))
			for _, re := range resolved {
				p.logger.Info("entity resolution fallback resolved candidate", "article_id", a.ID,
					"candidate", re.CandidateText, "canonical_name", re.CanonicalName, "type", re.Type, "confidence", re.Confidence)
			}
		}
	}

	a.PrimaryEntity = rep.PrimaryEntity

	windowStart := now.Add(-p.clusterCandidateWindow())
	candidates, err := p.clusters.FindCandidatesByEventKeys(ctx, rep.ApproximateKeys, windowStart, a.ID, p.clusterCandidateLimit())
	if err != nil {
		return clustering.Decision{}, fmt.Errorf("process cycle: find clustering candidates for %s: %w", a.ID, err)
	}
	applyDedupHint(candidates, dedupDecision)

	decision := clustering.DecideJoin(rep, candidates, p.cascade.Cluster.Decide, now)
	if decision.Action != clustering.ActionJoin {
		joined, ok, err := p.lexicalJoin(ctx, *a, decision, now)
		if err != nil {
			return clustering.Decision{}, err
		}
		if ok {
			decision = joined
		}
	}

	var clusterID string
	memberRole := clustering.MemberRoleFirstReport
	switch decision.Action {
	case clustering.ActionJoin:
		clusterID = decision.ClusterID
		memberRole = decision.MemberRole
		if err := p.clusters.AddMember(ctx, clusterID, a.ID, memberRole, decision.Score, now); err != nil {
			return clustering.Decision{}, fmt.Errorf("process cycle: join cluster %s for %s: %w", clusterID, a.ID, err)
		}
	default: // clustering.ActionCreate, clustering.ActionAmbiguous
		clusterID = uuid.NewString()
		if err := p.clusters.CreateCluster(ctx, clusterID, a.ID, clustering.StatusOpen, now); err != nil {
			return clustering.Decision{}, fmt.Errorf("process cycle: create cluster for %s: %w", a.ID, err)
		}
	}
	a.ClusterID = clusterID
	a.ClusterMemberRole = string(memberRole)

	if err := p.clusters.SaveRepresentation(ctx, rep, p.cascade.Cluster.RulesVersion, now); err != nil {
		return clustering.Decision{}, fmt.Errorf("process cycle: save representation for %s: %w", a.ID, err)
	}
	if len(rawUnknownEntities) > 0 {
		// best-effort: unknown-entity persistence must never be a hard
		// dependency for article processing — a failure here is logged and dropped, not
		// returned, unlike every other call in this function.
		//
		// Caveat: this call runs inside the article's per-cycle
		// UnitOfWork transaction, via the same executor(ctx, r.db)
		// mechanism every other repository call in this function uses. A
		// statement-scoped failure (e.g. a constraint/FK violation) is
		// safely swallowed here as intended, but a transaction-aborting
		// failure class would still resurface moments later as a
		// confusing, differently-attributed error from the next call
		// (SaveEventKeys). This can't be fixed by moving the call
		// outside the transaction: the articles row the new table's
		// foreign key references isn't committed yet at this point.
		if err := p.unknownEntities.SaveOccurrences(ctx, a.ID, rawUnknownEntities, p.cascade.Cluster.RulesVersion, now); err != nil {
			p.logger.Warn("save unknown entity occurrences", "article_id", a.ID, "error", err)
		}
	}
	if err := p.clusters.SaveEventKeys(ctx, a.ID, clusterID, rep.ApproximateKeys, now); err != nil {
		return clustering.Decision{}, fmt.Errorf("process cycle: save event keys for %s: %w", a.ID, err)
	}
	if err := p.clusters.SaveDecision(ctx, a.ID, clusterID, decision); err != nil {
		return clustering.Decision{}, fmt.Errorf("process cycle: save clustering decision for %s: %w", a.ID, err)
	}

	if decision.Action == clustering.ActionJoin {
		if err := p.maybeUpdateRepresentative(ctx, *a, rep, decision, candidates, now); err != nil {
			return clustering.Decision{}, err
		}
	}

	return decision, p.appendClusterEvent(ctx, a.ID, cycleID, now, decision, explainClusterDecision(decision, memberRole))
}

// maybeUpdateRepresentative decides whether the just-joined article should
// replace its cluster's representative (clustering.ReplacesCurrent),
// calling ClusterRepository.UpdateRepresentative only when it says to.
//
// The "current representative" ReplacesCurrent needs is approximated by
// winningClusterCandidate — the same-cluster candidate clustering.Compare
// itself ranked highest when deciding to join — rather than the cluster's
// actual, separately-tracked representative_article_id: ClusterRepository
// (finalized by an earlier task) exposes no "fetch this cluster's
// designated representative's Representation" query, only
// FindCandidatesByEventKeys' event-key-matched candidate set. In practice
// the two coincide whenever the representative's own event keys are still
// within the candidate search window/limit (the common case for an
// actively-clustering event); when they diverge, this proxy is a
// documented approximation, not a correctness bug — see this task's report
// for the reasoning and a note for a later task to consider adding a
// dedicated lookup.
//
// IsPrimarySource is sourced from sourcePolicyResolver.Resolve(...).
// PrefersAsRepresentative() — the per-source RepresentativePreference a
// Phase 12 source policy configures (Task 12), which is a purpose-built
// "should this source's articles be preferred as the cluster's
// representative" signal. This is deliberately a different source from
// Classification.TrustedSourceIDs (used just below by editorial.Evaluate,
// and above in processOne for the relevance-classification bypass/Telegram-
// labeling signal): a source can be pre-vetted as always-relevant without
// being a preferred representative, and vice versa, so the two lists are
// resolved and applied independently. ClickbaitPenalty has no computed
// source at all and is left at its zero value (no penalty) for both sides.
// Editorial uses editorial.Evaluate; passing a zero-value article.Article
// for the "current" side (whose full article.Article isn't available here,
// only its persisted Representation) is safe because ExtractFeatures
// tolerates every field being empty (see domain/editorial's own tests) — it
// just yields a sparser feature vector, not an error.
func (p *ProcessCycle) maybeUpdateRepresentative(ctx context.Context, a article.Article, rep eventsignals.Representation, decision clustering.Decision, candidates []clustering.Candidate, now time.Time) error {
	currentCand, ok := winningClusterCandidate(rep, candidates, decision.ClusterID, p.cascade.Cluster.Decide)
	if !ok {
		return nil
	}

	current := clustering.RepresentativeCandidate{
		ArticleID:       currentCand.ArticleID,
		Representation:  currentCand.Representation,
		IsPrimarySource: p.sourcePolicyResolver.Resolve(currentCand.Representation.SourceID).PrefersAsRepresentative(),
		Editorial:       editorial.Evaluate(article.Article{}, p.cascade.Classification.TrustedSourceIDs[currentCand.Representation.SourceID], p.editorialModel),
	}
	incoming := clustering.RepresentativeCandidate{
		ArticleID:       a.ID,
		Representation:  rep,
		IsPrimarySource: p.sourcePolicyResolver.Resolve(a.Source.ID.String()).PrefersAsRepresentative(),
		Editorial:       editorial.Evaluate(a, p.cascade.Classification.TrustedSourceIDs[a.Source.ID.String()], p.editorialModel),
	}

	replaces, score := clustering.ReplacesCurrent(current, incoming, p.cascade.Cluster.Representative, now)
	if !replaces {
		return nil
	}
	if err := p.clusters.UpdateRepresentative(ctx, decision.ClusterID, a.ID, score.Score, score.Evidence, p.cascade.Cluster.RulesVersion, now); err != nil {
		return fmt.Errorf("process cycle: update representative for cluster %s: %w", decision.ClusterID, err)
	}
	return nil
}

// appendClusterEvent records the clustering-specific audit trail entry
// (event_type "clustering") for articleID, separate from the eventual
// accept/reject event — see the plan's "record clustering audit in
// article_processing_events" requirement.
func (p *ProcessCycle) appendClusterEvent(ctx context.Context, articleID, cycleID string, now time.Time, d clustering.Decision, detail string) error {
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID:    articleID,
		OccurredAt:   now,
		FromStatus:   article.StatusClusterCandidate,
		ToStatus:     article.StatusClustered,
		EventType:    "clustering",
		ReasonCode:   string(d.Action),
		ReasonDetail: detail,
		Score:        d.Score,
		CycleID:      cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append clustering event for %s: %w", articleID, err)
	}
	return nil
}

// appendEditorialEvent records an editorial-classifier audit trail entry
// ("editorial_shadow", "editorial_ambiguous", "editorial_canary_shadow",
// "editorial_canary_shadow_would_reject") for a. Unlike
// appendClusterEvent, this is never a real status transition — a's status
// doesn't change here — so FromStatus and ToStatus are both a's current
// status, distinguishing this from a genuine lifecycle move.
//
// Mode/ModelChecksum/RejectThreshold carry the same model-identity
// attribution rejectEditorialScored writes for terminal rejects: without
// them every non-terminal editorial event (i.e. 100% of shadow-mode
// traffic, which is what production actually runs today) would land with
// mode=” / model_checksum=”, making the persisted audit trail unable
// to answer "which mode and which model artifact produced this?".
func (p *ProcessCycle) appendEditorialEvent(ctx context.Context, a article.Article, cycleID string, now time.Time, eventType string, score editorial.Score) error {
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: a.Status, ToStatus: a.Status,
		EventType: eventType, ReasonDetail: explainEditorial(score),
		ClassifierVersion: a.ClassifierVersion, Score: score.Value, Threshold: a.QualityThreshold,
		RejectThreshold: p.editorialRejectThreshold(), Mode: p.cascade.Editorial.Mode,
		ModelChecksum: p.editorialChecksum(), CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append %s event for %s: %w", eventType, a.ID, err)
	}
	return nil
}

// editorialChecksum and editorialRejectThreshold read the loaded model's
// identity defensively: editorial.mode: disabled never loads a model, and
// the veto layer can gate an article before any model-dependent branch
// runs, so a nil model must degrade to the zero value rather than panic.
func (p *ProcessCycle) editorialChecksum() string {
	if p.editorialModel == nil {
		return ""
	}
	return p.editorialModel.Checksum
}

func (p *ProcessCycle) editorialRejectThreshold() float64 {
	if p.editorialModel == nil {
		return 0
	}
	return p.editorialModel.Thresholds.Reject
}

// explainEditorial renders an editorial.Score as a single human-readable
// line for article_processing_events.reason_detail — mirroring
// explainVerdict/explainAcceptedVerdict's pattern from classification.
func explainEditorial(score editorial.Score) string {
	return fmt.Sprintf("editorial score %.1f (zone %s, model %s)", score.Value, score.Zone, score.ModelVersion)
}

// CanaryEnforced deterministically decides whether articleID falls in
// editorial.mode=canary's enforced arm: FNV-1a hash of articleID, mod
// 100, compared against percent (1-99, validated by
// EditorialRolloutConfig.Validate). Same articleID always yields the
// same answer for a given percent, across restarts and process
// instances — no per-run randomness, so a canary's enforced/shadow
// split is reproducible and auditable from persisted events alone.
func CanaryEnforced(articleID string, percent int) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(articleID))
	return int(h.Sum32()%100) < percent
}

func (p *ProcessCycle) accept(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, fingerprint string, text normalization.Representations, cycleID string, now time.Time, result *in.ProcessCycleResult, classificationDetail string) error {
	fromStatus := a.Status
	a.Status = article.StatusPendingPublication
	a.StatusReason = ""

	if p.currentMatchStateOK {
		if a.Metadata == nil {
			a.Metadata = map[string]string{}
		}
		a.Metadata["match_state_kind"] = "live"
		a.Metadata["match_id"] = p.currentMatchState.MatchID
		a.Metadata["match_home_team"] = p.currentMatchState.HomeTeam
		a.Metadata["match_away_team"] = p.currentMatchState.AwayTeam
		a.Metadata["match_phase"] = string(p.currentMatchState.Phase)
	}

	payload, err := json.Marshal(map[string]string{"title": a.Title, "canonical_url": a.CanonicalURL})
	if err != nil {
		return fmt.Errorf("process cycle: marshal outbox payload for %s: %w", a.ID, err)
	}

	err = p.uow.Do(ctx, func(txCtx context.Context) error {
		// Exact identity is always persisted, for every source: "unconditional
		// publish" never means "may republish the same canonical URL".
		if err := p.dedup.Save(txCtx, fingerprint, a.ID); err != nil {
			return fmt.Errorf("save fingerprint: %w", err)
		}
		if err := p.saveAliases(txCtx, a, text); err != nil {
			return err
		}
		if err := p.articles.Save(txCtx, a); err != nil {
			return fmt.Errorf("save article: %w", err)
		}
		lane := p.lanePolicy.LaneFor(a.Source.ID.String(), a.Metadata)
		var enqueueErr error
		if laneOutbox, ok := p.outbox.(out.LaneOutboxRepository); ok {
			enqueueErr = laneOutbox.EnqueueLane(txCtx, a.ID, string(payload), lane, now)
		} else {
			enqueueErr = p.outbox.Enqueue(txCtx, a.ID, string(payload), now)
		}
		if enqueueErr != nil {
			return fmt.Errorf("enqueue outbox: %w", enqueueErr)
		}
		if err := p.decisions.Append(txCtx, out.Event{
			ArticleID:         a.ID,
			OccurredAt:        now,
			FromStatus:        fromStatus,
			ToStatus:          a.Status,
			EventType:         "accept",
			ReasonDetail:      classificationDetail,
			ClassifierVersion: a.ClassifierVersion,
			DictionaryVersion: a.DictionaryVersion,
			Score:             a.EditorialScore,
			CycleID:           cycleID,
		}); err != nil {
			return err
		}
		if state, ok := matchstate.FromMetadata(a.Metadata); ok && state.IsTerminal() {
			if repo, ok := p.outbox.(out.TerminalSupersessionRepository); ok {
				items, err := repo.SupersedeClaimableMatch(txCtx, state.MatchID, a.ID, now)
				if err != nil {
					return fmt.Errorf("supersede match %s: %w", state.MatchID, err)
				}
				for _, item := range items {
					detail, _ := json.Marshal(map[string]string{
						"final_article_id": a.ID, "match_id": state.MatchID, "phase": string(state.Phase),
					})
					if err := p.decisions.Append(txCtx, out.Event{
						ArticleID: item.ArticleID, OccurredAt: now,
						FromStatus: article.Status(item.FromStatus), ToStatus: article.StatusDiscarded,
						EventType: "superseded", ReasonCode: "terminal_match_state",
						ReasonDetail: string(detail), CycleID: cycleID,
					}); err != nil {
						return fmt.Errorf("append supersession event for %s: %w", item.ArticleID, err)
					}
					result.Superseded++
				}
				if len(items) > 0 {
					p.logger.Info("terminal match state superseded claimable publications", "final_article_id", a.ID, "match_id", state.MatchID, "superseded", len(items))
				}
			}
		}
		if identity.Key == "" {
			return nil
		}
		return p.seenItems.Finalize(txCtx, identity, string(a.Status), contentHash, a.CanonicalURL, now)
	})
	if err != nil {
		return fmt.Errorf("process cycle: persist accepted article %s: %w", a.ID, err)
	}

	p.logger.Info("article accepted", "cycle_id", cycleID, "article_id", a.ID, "cluster_id", a.ClusterID,
		"source", a.Source.ID, "decision", "accept", "reason", classificationDetail,
		"classifier_version", a.ClassifierVersion, "dictionary_version", a.DictionaryVersion)

	result.Accepted++
	return nil
}

func (p *ProcessCycle) reject(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, status article.Status, eventType, reason string) error {
	fromStatus := a.Status
	a.Status = status
	a.StatusReason = reason
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save rejected article %s: %w", a.ID, err)
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID:    a.ID,
		OccurredAt:   now,
		FromStatus:   fromStatus,
		ToStatus:     status,
		EventType:    eventType,
		ReasonCode:   eventType,
		ReasonDetail: reason,
		CycleID:      cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article rejected", "cycle_id", cycleID, "article_id", a.ID, "cluster_id", a.ClusterID,
		"source", a.Source.ID, "decision", string(status), "reason", reason,
		"classifier_version", a.ClassifierVersion, "dictionary_version", a.DictionaryVersion)
	return nil
}

// retainNoPublish saves a as StatusRetainedNoPublish — a valid, clustered
// contribution that source policy (Gate B) decided should not itself
// reach the outbox. Unlike reject, this is not a failure signal: the
// article's representative-replacement effect (maybeUpdateRepresentative,
// already run inside cluster()) stands regardless.
func (p *ProcessCycle) retainNoPublish(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, decision sourcepolicy.Decision) error {
	fromStatus := a.Status
	a.Status = article.StatusRetainedNoPublish
	a.StatusReason = string(decision.Code)
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save retained-no-publish article %s: %w", a.ID, err)
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: a.Status,
		EventType: "source_policy_retain", ReasonCode: string(decision.Code), ReasonDetail: decision.Explanation, CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article retained without publishing", "cycle_id", cycleID, "article_id", a.ID,
		"cluster_id", a.ClusterID, "source", a.Source.ID, "decision", string(decision.Code), "reason", decision.Explanation)
	return nil
}

// gameInProgress reports whether the match-state provider confirmed, for
// this cycle, that the team's game is being played (not pregame, not final).
func (p *ProcessCycle) gameInProgress() bool {
	if !p.currentMatchStateOK {
		return false
	}
	switch p.currentMatchState.Phase {
	case matchstate.Kickoff, matchstate.Q1, matchstate.Q2, matchstate.Halftime, matchstate.Q3, matchstate.Q4:
		return true
	}
	return false
}

// lexicalJoin looks for an open cluster holding a recent article that tells
// the same story as a in other words, for articles event signals could not
// place (most of the traffic has no event type to match on). It returns the
// join decision to use instead of prior, or ok=false.
func (p *ProcessCycle) lexicalJoin(ctx context.Context, a article.Article, prior clustering.Decision, now time.Time) (clustering.Decision, bool, error) {
	policy := p.cascade.LexicalJoin
	if !policy.Enabled || p.recentStories == nil || p.cascade.StoryGuard.Exempt(a.Title) {
		return clustering.Decision{}, false, nil
	}
	text := p.cascade.Normalization.Text
	tokens := storyguard.Tokenize(a.Title, text)
	if len(tokens) == 0 {
		return clustering.Decision{}, false, nil
	}
	stories, err := p.recentStories.ListRecentClustered(ctx, now.Add(-policy.Window), a.ID, 2000)
	if err != nil {
		return clustering.Decision{}, false, fmt.Errorf("process cycle: list recent clustered articles for %s: %w", a.ID, err)
	}
	recent := make([]storyguard.Recent, 0, len(stories))
	for _, s := range stories {
		if s.ClusterID == "" {
			continue
		}
		recent = append(recent, storyguard.Recent{ArticleID: s.ArticleID, ClusterID: s.ClusterID, Title: s.Title, Tokens: p.titleTokens.get(s.ArticleID, s.Title, text)})
	}
	best, ratio, ok := policy.BestMatch(tokens, recent)
	if !ok {
		return clustering.Decision{}, false, nil
	}
	return clustering.Decision{
		Action:       clustering.ActionJoin,
		ClusterID:    best.ClusterID,
		MemberRole:   clustering.MemberRoleRelatedCoverage,
		Score:        ratio,
		Evidence:     []clustering.Evidence{clustering.Evidence(fmt.Sprintf("lexical: wording overlaps %.0f%% with article %s in the cluster", 100*ratio, best.ArticleID))},
		Alternatives: prior.Alternatives,
		RulesVersion: prior.RulesVersion,
		DecidedAt:    now,
	}, true, nil
}

// storyRepeated reports whether the channel already carries the configured
// maximum number of messages about a's story, and which articles they are.
func (p *ProcessCycle) storyRepeated(ctx context.Context, a article.Article, text normalization.Representations, now time.Time) (bool, []string, error) {
	policy := p.cascade.StoryGuard
	if !policy.Enabled || p.recentStories == nil || policy.Exempt(a.Title) {
		return false, nil, nil
	}
	stories, err := p.recentStories.ListRecentlyPublishable(ctx, now.Add(-policy.Window), a.ID, 2000)
	if err != nil {
		return false, nil, fmt.Errorf("process cycle: list recent stories for %s: %w", a.ID, err)
	}
	recent := make([]storyguard.Recent, len(stories))
	for i, s := range stories {
		recent[i] = storyguard.Recent{ArticleID: s.ArticleID, Title: s.Title, Tokens: p.titleTokens.get(s.ArticleID, s.Title, p.cascade.Normalization.Text)}
	}
	exceeded, matched := policy.Exceeded(storyguard.Tokenize(a.Title, p.cascade.Normalization.Text), recent)
	return exceeded, matched, nil
}

// retainStoryRepeat saves a as StatusRetainedNoPublish because its story
// already reached the channel the maximum number of times.
func (p *ProcessCycle) retainStoryRepeat(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, matched []string) error {
	fromStatus := a.Status
	a.Status = article.StatusRetainedNoPublish
	a.StatusReason = "story_repeat_limit"
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save story-repeat retained article %s: %w", a.ID, err)
	}
	detail := fmt.Sprintf("story already published or queued %d times: %s", len(matched), strings.Join(matched, ","))
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: a.Status,
		EventType: "story_repeat_retain", ReasonCode: "story_repeat_limit", ReasonDetail: detail, CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article retained: story repeat limit", "cycle_id", cycleID, "article_id", a.ID,
		"cluster_id", a.ClusterID, "source", a.Source.ID, "matched", strings.Join(matched, ","))
	return nil
}

func (p *ProcessCycle) retainClusterAmbiguous(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, decision clustering.Decision) error {
	fromStatus := a.Status
	a.Status = article.StatusRetainedNoPublish
	a.StatusReason = "cluster_ambiguous"
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save cluster-ambiguous retained article %s: %w", a.ID, err)
	}
	detail := fmt.Sprintf("cluster ambiguity retained without publishing: score %.2f rules %s", decision.Score, decision.RulesVersion)
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: a.Status,
		EventType: "cluster_ambiguous_retain", ReasonCode: "cluster_ambiguous", ReasonDetail: detail, CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article retained for cluster ambiguity", "cycle_id", cycleID, "article_id", a.ID,
		"cluster_id", a.ClusterID, "source", a.Source.ID, "score", decision.Score, "rules_version", decision.RulesVersion)
	return nil
}

// recordSourcePolicyDecision persists the audit row for one Gate B
// evaluation. would_publish_standard is what today's pre-Phase-12
// pipeline would have done (always true here, since a plain
// MemberRoleDuplicate never reaches Gate B outside a retained near-dup —
// see Gate A). wouldPublishSpecial is the caller's pre-reservation Decide
// output — pure telemetry, deliberately a SEPARATE parameter
// from decision, because decision may itself have been overridden to
// CodeRejectCooldown/ShouldPublish:false by ResolveCooldownReservation
// (the OutcomeBlocked case) while wouldPublishSpecial must still report
// what sourcepolicy.Decide alone would have done. decision.Code/
// MemberRole/Novelty/Explanation, by contrast, DO reflect the final
// (possibly-overridden) decision — those describe what actually
// happened, not what Decide alone would have said. outcome is the
// differentiated cooldown-reservation-layer field.
func (p *ProcessCycle) recordSourcePolicyDecision(ctx context.Context, a article.Article, dedupDecision deduplication.Decision, decision sourcepolicy.Decision, wouldPublishSpecial bool, outcome sourcepolicy.ReservationOutcome, now time.Time) error {
	resolved := p.sourcePolicyResolver.Resolve(a.Source.ID.String())
	return p.sourcePolicies.Save(ctx, out.SourcePolicyDecisionRecord{
		ID:                   uuid.NewString(),
		ArticleID:            a.ID,
		SourceID:             a.Source.ID.String(),
		Authority:            string(resolved.Authority),
		PolicyVersion:        resolved.Version,
		ClusterID:            a.ClusterID,
		DedupMatchType:       string(dedupDecision.Type),
		MemberRole:           string(decision.MemberRole),
		Novelty:              string(decision.Novelty),
		DecisionCode:         string(decision.Code),
		WouldPublishStandard: true,
		WouldPublishSpecial:  wouldPublishSpecial,
		Mode:                 string(p.sourcePolicyResolver.Mode()),
		CooldownOutcome:      string(outcome),
		Explanation:          decision.Explanation,
		DecidedAt:            now,
	})
}

// rejectEditorial performs the same state transition as reject() (article
// status -> StatusRejectedQuality, one article_processing_events row) but,
// unlike the generic reject(), also carries the editorial Score/Threshold/
// ClassifierVersion that produced the decision on that same event row.
// Without this, an enforce-mode reject's audit event carried none of those
// three fields (only the plain-text ReasonDetail from explainEditorial),
// making the score effectively unqueryable/undisplayed — see
// editorial-review's report, which reads the Score column directly. A
// dedicated method (rather than threading score fields through the generic
// reject(), used by five other unrelated reject paths) keeps this the only
// place that couples article rejection to the editorial package.
func (p *ProcessCycle) rejectEditorial(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, score editorial.Score) error {
	return p.rejectEditorialScored(ctx, a, identity, contentHash, cycleID, now, score, "editorial:reject_score")
}

// rejectEditorialAmbiguous is rejectEditorial's counterpart for the
// enforce-mode ambiguous zone under AmbiguousPolicy == "reject": an
// ambiguous score is a terminal rejection, distinct from a score-reject
// only in its reason code (see the mode switch in processOne).
func (p *ProcessCycle) rejectEditorialAmbiguous(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, score editorial.Score) error {
	return p.rejectEditorialScored(ctx, a, identity, contentHash, cycleID, now, score, "editorial:ambiguous_reject")
}

// rejectEditorialScored is rejectEditorial's body, parameterized by
// reasonCode so score-reject and ambiguous-reject share one
// implementation and the same persistence discipline.
func (p *ProcessCycle) rejectEditorialScored(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, score editorial.Score, reasonCode string) error {
	fromStatus := a.Status
	reason := explainEditorial(score)
	a.Status = article.StatusRejectedQuality
	a.StatusReason = reason
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save rejected article %s: %w", a.ID, err)
	}
	if a.ClusterID != "" {
		if _, err := p.sourcePolicyCooldowns.Release(ctx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
			return fmt.Errorf("process cycle: release source policy cooldown for %s: %w", a.ID, err)
		}
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID:         a.ID,
		OccurredAt:        now,
		FromStatus:        fromStatus,
		ToStatus:          article.StatusRejectedQuality,
		EventType:         "editorial",
		ReasonCode:        reasonCode,
		ReasonDetail:      reason,
		ClassifierVersion: a.ClassifierVersion,
		Score:             score.Value,
		Threshold:         a.QualityThreshold,
		RejectThreshold:   p.editorialModel.Thresholds.Reject,
		Mode:              p.cascade.Editorial.Mode,
		ModelChecksum:     p.editorialModel.Checksum,
		CycleID:           cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article rejected", "cycle_id", cycleID, "article_id", a.ID, "cluster_id", a.ClusterID,
		"source", a.Source.ID, "decision", string(article.StatusRejectedQuality), "reason", reason, "reason_code", reasonCode,
		"classifier_version", a.ClassifierVersion, "dictionary_version", a.DictionaryVersion)
	return nil
}

// rejectEditorialVeto mirrors rejectEditorial's shape for a
// veto-triggered rejection — same terminal status (a veto is a
// deterministic instance of "editorial quality" rejection), distinct
// EventType/ReasonCode so it is never confused with a model-driven
// rejection in article_processing_events.
func (p *ProcessCycle) rejectEditorialVeto(ctx context.Context, a article.Article, identity ingestidentity.Identity, contentHash, cycleID string, now time.Time, v editorialveto.Result) error {
	fromStatus := a.Status
	reason := fmt.Sprintf("veto: %s (%v)", v.ReasonCode, v.MatchedTerms)
	a.Status = article.StatusRejectedQuality
	a.StatusReason = reason
	if err := p.articles.Save(ctx, a); err != nil {
		return fmt.Errorf("process cycle: save veto-rejected article %s: %w", a.ID, err)
	}
	if a.ClusterID != "" {
		if _, err := p.sourcePolicyCooldowns.Release(ctx, a.Source.ID.String(), a.ClusterID, a.ID, now); err != nil {
			return fmt.Errorf("process cycle: release source policy cooldown for %s: %w", a.ID, err)
		}
	}
	if err := p.decisions.Append(ctx, out.Event{
		ArticleID: a.ID, OccurredAt: now, FromStatus: fromStatus, ToStatus: article.StatusRejectedQuality,
		EventType: "editorial_veto", ReasonCode: v.ReasonCode, ReasonDetail: reason,
		Mode: p.cascade.Editorial.Mode, ModelChecksum: p.editorialChecksum(), CycleID: cycleID,
	}); err != nil {
		return fmt.Errorf("process cycle: append veto rejection event for %s: %w", a.ID, err)
	}
	if identity.Key != "" {
		if err := p.seenItems.Finalize(ctx, identity, string(a.Status), contentHash, a.CanonicalURL, now); err != nil {
			return fmt.Errorf("process cycle: finalize seen item for %s: %w", a.ID, err)
		}
	}
	p.logger.Info("article rejected by veto", "cycle_id", cycleID, "article_id", a.ID, "cluster_id", a.ClusterID,
		"source", a.Source.ID, "decision", string(article.StatusRejectedQuality), "reason", reason)
	return nil
}

func isOfficialAuthority(a sourcepolicy.AuthorityLevel) bool {
	switch a {
	case sourcepolicy.AuthorityOfficialTeam, sourcepolicy.AuthorityOfficialLeague,
		sourcepolicy.AuthorityOfficialPlayer, sourcepolicy.AuthorityOfficialStaff:
		return true
	}
	return false
}
