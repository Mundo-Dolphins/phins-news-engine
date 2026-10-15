// Package clustergate decides whether an accepted article may become (or
// remain within) its cluster's publication gate — the persistence-level
// guarantee that a cluster produces at most one initial publication,
// independent of sourcepolicy.Mode (see migration
// 0018_cluster_publication_gate.sql and ports/out.ClusterPublicationRepository).
// Decide is pure: it takes the already-computed CAS outcome
// (acquired) plus the article's clustering.MemberRole and the
// sourcepolicy.DecisionCode sourcepolicy.Decide already produced, and never
// does I/O or fabricates either input itself.
package clustergate

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

// Outcome is Decide's verdict for one article's cluster-gate evaluation.
type Outcome string

const (
	// OutcomeReservedInitial: the caller's CAS reservation attempt
	// succeeded — this article becomes (or re-becomes, after an earlier
	// reservation was released) its cluster's initial publication.
	OutcomeReservedInitial Outcome = "reserved_initial"
	// OutcomeAllowedExempt: the CAS reservation failed (the cluster
	// already has an active reservation), but role never needs to justify
	// novelty to publish anyway — see isExempt.
	OutcomeAllowedExempt Outcome = "allowed_exempt"
	// OutcomeAllowedUpdate: the CAS reservation failed, but sourcepolicy
	// already authorized this article as an explicit material update
	// (CodePublishOfficialConfirmation or CodePublishSubstantialUpdate).
	OutcomeAllowedUpdate Outcome = "allowed_update"
	// OutcomeDeniedDuplicate: the CAS reservation failed and neither of
	// the above applies — this article must not publish.
	OutcomeDeniedDuplicate Outcome = "denied_duplicate"
)

// Decide returns the cluster-gate outcome for one article. acquired is the
// result of the caller's CAS reservation attempt
// (ports/out.ClusterPublicationRepository.Reserve), which must always be
// attempted first, unconditionally, regardless of role — there is no
// separate "check first" step, the reservation attempt itself is the
// check. role is the clustering.MemberRole clustering.DecideJoin already
// assigned; policyCode is the sourcepolicy.DecisionCode sourcepolicy.Decide
// already computed for this article.
func Decide(acquired bool, role clustering.MemberRole, policyCode sourcepolicy.DecisionCode) Outcome {
	if acquired {
		return OutcomeReservedInitial
	}
	if isExempt(role) {
		return OutcomeAllowedExempt
	}
	if policyCode == sourcepolicy.CodePublishOfficialConfirmation || policyCode == sourcepolicy.CodePublishSubstantialUpdate {
		return OutcomeAllowedUpdate
	}
	return OutcomeDeniedDuplicate
}

// isExempt reports whether role never needs to prove a material update to
// publish on an already-gated cluster: the founding role, and the two
// roles that are commentary/reaction on an already-covered event rather
// than a republication of the news itself (per the confirmed
// scope), plus primary_source (never assigned by clustering.DecideJoin
// today — see that constant's own doc comment — but semantically the same
// "authoritative voice" case as first_report).
func isExempt(role clustering.MemberRole) bool {
	switch role {
	case clustering.MemberRoleFirstReport, clustering.MemberRoleAnalysis,
		clustering.MemberRoleReaction, clustering.MemberRolePrimarySource:
		return true
	default:
		return false
	}
}
