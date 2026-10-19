// Package in defines the driving ports: the operations an inbound adapter
// (a scheduler, a CLI command) can trigger on the application.
package in

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/storyguard"
)

// ProcessCycleResult summarizes the outcome of one fetch-process-publish cycle.
type ProcessCycleResult struct {
	Fetched   int
	Accepted  int
	Rejected  int
	Duplicate int
	// ClusterGateDenied counts articles rejected specifically by the
	// cluster publication gate — a subset of Rejected, broken
	// out for operator visibility since this gate has no config toggle to
	// audit against, unlike sourcepolicy's shadow-mode audit trail.
	ClusterGateDenied int
	// ClusterGateAllowedExempt counts articles that published on an
	// already-gated cluster because their role is exempt from the gate
	// (first_report/analysis/reaction/primary_source) — not itself a
	// duplicate-publication signal, but useful operator visibility into how
	// often the exempt carve-out fires, since a misclassified role here is
	// the most likely way a duplicate could still reach Telegram.
	ClusterGateAllowedExempt int
	// ClusterGateAllowedUpdate counts articles that published on an
	// already-gated cluster because sourcepolicy explicitly authorized them
	// as a material update (CodePublishOfficialConfirmation or
	// CodePublishSubstantialUpdate).
	ClusterGateAllowedUpdate int
	// CooldownReserved counts articles whose source-policy cooldown
	// reservation was acquired (sourcepolicy.OutcomeReserved) or
	// force-acquired for a correction (sourcepolicy.OutcomeForced) — see
	// ResolveCooldownReservation and the design doc.
	CooldownReserved int
	// CooldownBlocked counts articles whose final source-policy decision
	// is sourcepolicy.CodeRejectCooldown, whether from Decide's own
	// cooldownRemaining read or from ResolveCooldownReservation's
	// OutcomeBlocked (a lost CAS race).
	CooldownBlocked int
	// CooldownShadowWouldPublish counts sourcepolicy.OutcomeShadow rows
	// whose (unresolved) Decision.ShouldPublish is true — telemetry for
	// what mode: enforce would have reserved, never acted on.
	CooldownShadowWouldPublish int
	// Claimed counts items that got a fresh (or reclaimed-stale) seen_items
	// claim and ran the full pipeline this cycle.
	Claimed int
	// SeenSkipped counts items whose identity already has a terminal
	// outcome with unchanged content — the pipeline did not run at all.
	SeenSkipped int
	// Revised counts items whose identity already has a terminal outcome
	// but changed content, where that outcome was not eligible for
	// reprocessing (article.IsReprocessable) — recorded for audit, not
	// reprocessed.
	Revised int
	// InFlightSkipped counts items claimed by another not-yet-finalized,
	// not-yet-stale worker — skipped this poll, retried next cycle.
	InFlightSkipped int
	// IdentityCollisions counts items whose remote-id-based identity
	// resolved to a previously-seen key but with a canonical URL on a
	// different host — treated as a distinct identity rather than merged
	// (see ingestidentity.IsCollision).
	IdentityCollisions int
	// ClaimErrors counts seen_items claim/finalize calls that returned an
	// error.
	ClaimErrors int
	// Superseded counts claimable earlier game publications invalidated by a
	// terminal final article.
	Superseded int
	// ByPlatform breaks Claimed/SeenSkipped/Revised/IdentityCollisions/
	// ClaimErrors down per source.Type string value.
	ByPlatform map[string]PlatformIngestMetrics
}

// PlatformIngestMetrics is ProcessCycleResult.ByPlatform's per-platform
// breakdown.
type PlatformIngestMetrics struct {
	Claimed            int
	SeenSkipped        int
	Revised            int
	IdentityCollisions int
	ClaimErrors        int
}

// BumpPlatform applies apply to platform's entry in r.ByPlatform,
// initializing the map and the entry on first use. Defined here (rather
// than inlined at each call site in usecase.ProcessCycle) because
// ProcessCycleResult is a plain value the usecase package receives by
// pointer — this keeps the read-modify-write map-of-structs dance in one
// place.
func (r *ProcessCycleResult) BumpPlatform(platform string, apply func(*PlatformIngestMetrics)) {
	if r.ByPlatform == nil {
		r.ByPlatform = map[string]PlatformIngestMetrics{}
	}
	pm := r.ByPlatform[platform]
	apply(&pm)
	r.ByPlatform[platform] = pm
}

// ProcessCycle runs one full fetch-process-publish cycle.
type ProcessCycle interface {
	Run(ctx context.Context) (ProcessCycleResult, error)
}

// NormalizationSettings bundles the URL and text normalization tunables
// ProcessCycle needs, built once by config.Config.DedupCascade().
type NormalizationSettings struct {
	URL       normalization.URLConfig
	Resolvers []normalization.KnownLinkResolver
	Text      normalization.TextConfig
}

// DedupSettings bundles the near-duplicate detection tunables ProcessCycle
// needs beyond its repositories.
type DedupSettings struct {
	// Enabled gates only the near-duplicate reject decision; exact-duplicate
	// detection (canonical-URL fingerprint and alias matching) is a separate,
	// always-on mechanism from an earlier phase and is unaffected by this
	// flag.
	Enabled               bool
	Method                string
	DefaultThreshold      float64
	ThresholdBySourceType map[string]float64
	ShortTextMaxTokens    int
	ShortTextThreshold    float64
	WindowHours           int
	MaxCandidates         int
	MinSharedTerms        int
	FieldsUsed            []string
	ContentTypeExceptions map[article.ContentType]bool
	AttributeComboWindow  time.Duration
	RulesVersion          string
	// EventThreshold is the pre-Phase-6 shingle-similarity band boundary
	// deduplication.Decide uses to distinguish DuplicateTypeSameEvent from
	// DuplicateTypeNone (see deduplication.ThresholdConfig.EventThreshold).
	// It lived on ClusterSettings before Phase 6 (when clustering itself was
	// shingle-score-based); it moved here because it is, and always was,
	// purely a dedup-banding threshold — the event-signal clustering this
	// phase adds (ClusterSettings.Decide) never reads it. Kept only for
	// deduplication.Decide's Type label and dedupeval/CLI backward
	// compatibility; it has no effect on clustering.DecideJoin.
	EventThreshold float64
}

// ClusterSettings bundles the event-clustering tunables ProcessCycle needs.
// LeagueEntitySource supplies league-scope entity rules that ProcessCycle merges
// into the clustering rules. Refresh is called once per cycle; Entities returns
// an immutable slice (nil when the feature is disabled).
type LeagueEntitySource interface {
	Refresh(now time.Time)
	Entities() []eventsignals.EntityRule
}

type ClusterSettings struct {
	// Enabled gates the join-vs-create decision; when false every accepted
	// article still gets its own new cluster (ADR 0009's
	// one-cluster-per-article invariant) instead of running
	// eventsignals.Extract/clustering.DecideJoin at all — matching the
	// pre-Phase-6 fallback behavior.
	Enabled bool

	// Rules is the eventsignals vocabulary (entities, action/content-signal
	// terms, event windows) ProcessCycle uses to derive each accepted
	// article's eventsignals.Representation, typically built via
	// clusteringrules.Load(...).ToEventSignalsRules().
	Rules eventsignals.Rules

	// LeagueEntities, when non-nil, adds league-wide player rules to Rules.
	// Their matches are mentions only and never influence clustering.
	LeagueEntities LeagueEntitySource

	// Decide configures clustering.DecideJoin's join/ambiguous/create
	// thresholds and per-signal weights, typically built via
	// clusteringrules.Load(...).ToClusteringConfig().
	Decide clustering.Config

	// Representative configures clustering.RankRepresentative/
	// ReplacesCurrent's per-signal weights and switch thresholds. See
	// clusteringrules.RulesFile.ToRepresentativeConfig for the known gap:
	// clusteringrules.RulesFile.Representative (the YAML loader) only
	// carries 3 of RepresentativeConfig's 9 weights plus the 2 switch
	// thresholds and domain priorities; the remaining 6 weights, the
	// freshness window, and the default domain priority use small,
	// explicitly-labeled constants there until a later task extends the
	// YAML schema.
	Representative clustering.RepresentativeConfig

	// CandidateLimit caps how many rows
	// ClusterRepository.FindCandidatesByEventKeys returns per lookup,
	// mirroring DedupSettings.MaxCandidates' role for the separate,
	// shingle-based near-duplicate candidate search. A value <= 0 falls
	// back to a package-level default (see process_cycle.go).
	CandidateLimit int

	// RulesVersion identifies the clustering rules revision in effect,
	// mirroring Rules.Version/Decide.RulesVersion (kept here too so a
	// caller that only has ClusterSettings — not the full Rules/Decide
	// structs — can still audit/enforce which revision is active).
	RulesVersion string
}

// ClassificationSettings bundles the team-relevance classification
// tunables ProcessCycle needs.
type ClassificationSettings struct {
	// Enabled gates the real classifier; while false, ProcessCycle keeps
	// the placeholder behavior from phases 1-3 (always related, dictionary
	// version "placeholder-v0", never rejects).
	Enabled bool
	// TrustedSourceIDs are source.Source.ID values that skip scoring
	// entirely and are always treated as related. Bypassed
	// articles still go through deduplication (already ran, earlier in the
	// cascade) and, in a later phase, editorial scoring — only the
	// relevance decision itself is skipped.
	TrustedSourceIDs map[string]bool
	// ContextSourceIDs are narrowly scoped sources where a Isotopes/Topes
	// title mention can be treated as unambiguous team context. They do not
	// bypass scoring: posts without positive evidence remain rejected.
	ContextSourceIDs map[string]bool
	// Anchor demotes a related verdict that has no team-naming evidence.
	// Trusted sources are never subject to it.
	Anchor classification.AnchorPolicy
}

// EditorialSettings bundles the editorial-classifier tunable ProcessCycle
// needs: which of disabled/shadow/canary/enforce mode is active, and the policy
// for handling articles with ambiguous editorial scores. The loaded
// *editorial.Model itself is a separate ProcessCycle constructor
// parameter (Task 25), not part of this config value object — mirroring
// how the dictionary.Matcher is a separate parameter from
// ClassificationSettings, since loading it can fail and doesn't belong in
// an error-free config-conversion method (see config.go's DedupCascade
// doc comment).
type EditorialSettings struct {
	Mode                          string // "disabled" | "shadow" | "canary" | "enforce"
	AmbiguousPolicy               string // "reject" | "accept"
	CanaryPercent                 int    // 1-99, only meaningful when Mode == "canary"
	ModelBypassSourceIDs          map[string]bool
	UnconditionalPublishSourceIDs map[string]bool
}

// SeenItemsSettings bundles the stable-ingestion-identity tunable
// ProcessCycle needs beyond its SeenItemsRepository.
type SeenItemsSettings struct {
	// ReclaimAfter bounds how long an unfinalized seen_items claim is
	// honored as in-flight before a later poll is allowed to reclaim it —
	// crash recovery for a worker that died mid-processing.
	ReclaimAfter time.Duration
}

// DedupCascadeConfig bundles every Phase 3 tunable ProcessCycle needs as a
// single constructor parameter, the same pattern RetentionWindows already
// established for RunRetention.
type DedupCascadeConfig struct {
	Classification ClassificationSettings
	Normalization  NormalizationSettings
	Dedup          DedupSettings
	Cluster        ClusterSettings
	Editorial      EditorialSettings
	SeenItems      SeenItemsSettings
	// StoryGuard caps how many messages one story may have in the channel.
	StoryGuard storyguard.Policy
	// PublishAmbiguousClusters lets an article whose clustering decision was
	// ambiguous continue as the first report of a new cluster instead of
	// being retained without publishing. The story guard still bounds
	// repeats.
	PublishAmbiguousClusters bool
	// LexicalJoin lets an article that event signals could not place join the
	// open cluster of a recent article that tells the same story in other
	// words (Enabled, Window and the matching thresholds are used; MaxPerStory
	// is not).
	LexicalJoin storyguard.Policy
}
