// domain/sourcepolicy/decide.go
package sourcepolicy

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
)

// DecisionCode is Decide's verdict code. CodeRejectExactDuplicate exists
// only so the type itself documents the invariant that exact duplicates
// are a possible *concept* here — Decide never returns it, because
// process_cycle.go never calls Decide for an exact duplicate: those are
// rejected in Step 2, entirely before Decide's caller (Step 6/Gate B) can
// run. The invariant is enforced by pipeline position, not a runtime
// branch that could be misconfigured away.
type DecisionCode string

const (
	CodeRejectExactDuplicate          DecisionCode = "reject_exact_duplicate"
	CodeRetainClusterMember           DecisionCode = "retain_cluster_member"
	CodeOfficialConfirmationNoPublish DecisionCode = "official_confirmation_no_publish"
	CodePublishOfficialConfirmation   DecisionCode = "publish_official_confirmation"
	CodePublishSubstantialUpdate      DecisionCode = "publish_substantial_update"
	CodeRejectInsufficientNovelty     DecisionCode = "reject_insufficient_novelty"
	CodeRejectCooldown                DecisionCode = "reject_cooldown"
	CodeRequireReview                 DecisionCode = "require_review"
	CodeStandard                      DecisionCode = "standard"

	// CodeWouldRetainNearDuplicate is used only by process_cycle.go's Gate
	// A, only in ModeShadow, and only when policy.RetainNearDuplicates is
	// true: enforce mode would have let this near-duplicate fall through
	// to clustering instead of auto-rejecting it, but shadow mode kept the
	// standard reject behavior. It is never returned by Decide (Decide
	// runs downstream of clustering, at Gate B, and Gate A's shadow-mode
	// case never reaches it) — clustering hasn't run yet at Gate A, so the
	// eventual MemberRole, Novelty, and publish outcome genuinely are not
	// known; the audit row recorded for this code must not claim otherwise.
	CodeWouldRetainNearDuplicate DecisionCode = "would_retain_near_duplicate"
)

// Decision is the outcome of an editorial policy evaluation for one
// cluster member, downstream of clustering.DecideJoin's factual
// MemberRole. ShouldPublish is what process_cycle.go's Gate B actually
// acts on; every other field exists for the audit trail
// (source_policy_decisions).
type Decision struct {
	Code          DecisionCode
	Explanation   string
	Novelty       NoveltyLevel
	MemberRole    clustering.MemberRole
	ShouldPublish bool
}

// Decide evaluates policy for role, a MemberRole clustering.DecideJoin has
// already assigned (Decide never fabricates or overrides it). cooldownRemaining
// is the time left in policy.EventCooldown for this (source, cluster) pair,
// as looked up by the caller — zero or negative means no cooldown is
// active. Decide is pure and has no I/O.
func Decide(role clustering.MemberRole, policy Policy, cooldownRemaining time.Duration, now time.Time) Decision {
	novelty := ClassifyNovelty(role)

	switch role {
	case clustering.MemberRoleFirstReport, clustering.MemberRoleAnalysis,
		clustering.MemberRoleReaction, clustering.MemberRolePrimarySource, clustering.MemberRoleRelatedCoverage:
		// Not a duplicate/near-duplicate situation at all — policy has
		// nothing to decide, publish exactly as every article does today.
		return Decision{Code: CodeStandard, Explanation: "no policy-relevant duplication signal", Novelty: novelty, MemberRole: role, ShouldPublish: true}

	case clustering.MemberRoleDuplicate:
		if !policy.RetainNearDuplicates {
			return Decision{Code: CodeStandard, Explanation: "policy does not retain near-duplicates; standard rejection applies upstream", Novelty: novelty, MemberRole: role, ShouldPublish: true}
		}
		return Decision{Code: CodeRetainClusterMember, Explanation: "near-duplicate retained as cluster member per policy, not published", Novelty: novelty, MemberRole: role, ShouldPublish: false}

	case clustering.MemberRoleConfirmation:
		if !policy.AllowOfficialConfirmation {
			return Decision{Code: CodeRetainClusterMember, Explanation: "confirmation retained as cluster member; policy does not allow official confirmation handling", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		if cooldownRemaining > 0 {
			return Decision{Code: CodeRejectCooldown, Explanation: "official confirmation within event cooldown window", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		if !policy.PublishConfirmationWithoutNewInformation {
			return Decision{Code: CodeOfficialConfirmationNoPublish, Explanation: "official confirmation retained, representative may still be replaced; publish-without-new-information disabled by policy", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		return Decision{Code: CodePublishOfficialConfirmation, Explanation: "official confirmation published per policy", Novelty: novelty, MemberRole: role, ShouldPublish: true}

	case clustering.MemberRoleSubstantialUpdate:
		if !policy.AllowSubstantialUpdate {
			return Decision{Code: CodeRejectInsufficientNovelty, Explanation: "substantial update detected but policy does not allow substantial-update publication", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		if cooldownRemaining > 0 {
			return Decision{Code: CodeRejectCooldown, Explanation: "substantial update within event cooldown window", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		return Decision{Code: CodePublishSubstantialUpdate, Explanation: "substantial update published per policy", Novelty: novelty, MemberRole: role, ShouldPublish: true}

	case clustering.MemberRoleCorrection:
		if !policy.AllowSubstantialUpdate {
			return Decision{Code: CodeRejectInsufficientNovelty, Explanation: "correction detected but policy does not allow substantial-update/correction publication", Novelty: novelty, MemberRole: role, ShouldPublish: false}
		}
		// Corrections deliberately bypass the cooldown check: factual
		// correctness is not something a pacing control should delay.
		return Decision{Code: CodePublishSubstantialUpdate, Explanation: "official correction published per policy, bypassing cooldown", Novelty: novelty, MemberRole: role, ShouldPublish: true}

	default:
		return Decision{Code: CodeRequireReview, Explanation: "unrecognized member role for source policy evaluation", Novelty: novelty, MemberRole: role, ShouldPublish: false}
	}
}
