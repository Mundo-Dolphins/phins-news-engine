package sourcepolicy

import "github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"

// ReservationOutcome is ResolveCooldownReservation's verdict for what the
// reservation layer (ports/out.SourcePolicyCooldownRepository, see the
// design doc) actually did with a Decide-produced Decision.
type ReservationOutcome string

const (
	// OutcomeNotApplicable: decision.Code isn't cooldown-relevant, or
	// ShouldPublish was already false from Decide's own (possibly stale)
	// cooldownRemaining read — no reservation attempt was made.
	OutcomeNotApplicable ReservationOutcome = "not_applicable"
	// OutcomeShadow: mode != ModeEnforce — the decision is recorded for
	// telemetry only, no reservation was ever attempted.
	OutcomeShadow ReservationOutcome = "shadow"
	// OutcomeReserved: enforce mode, a normal (non-correction) CAS
	// reservation attempt succeeded.
	OutcomeReserved ReservationOutcome = "reserved"
	// OutcomeForced: enforce mode, MemberRoleCorrection — the caller
	// always force-overwrites the reservation for a correction, bypassing
	// the CAS entirely (corrections bypass the cooldown check by design).
	OutcomeForced ReservationOutcome = "forced"
	// OutcomeBlocked: enforce mode, a normal CAS reservation attempt was
	// lost to an active window — decision is overridden to
	// CodeRejectCooldown even though Decide's own (stale) read said
	// ShouldPublish: true.
	OutcomeBlocked ReservationOutcome = "blocked_by_cooldown"
)

// IsCooldownRelevant reports whether code is one Decide produces only
// after checking cooldownRemaining for a role the cooldown reservation
// applies to (CodeRejectCooldown itself is deliberately excluded here —
// it already means "no reservation to attempt, Decide's own read already
// says blocked").
func IsCooldownRelevant(code DecisionCode) bool {
	return code == CodePublishOfficialConfirmation || code == CodePublishSubstantialUpdate
}

// ResolveCooldownReservation is Decide's necessary second pass: decision
// is Decide's pure output (computed from a cooldownRemaining read that
// may already be stale by the time the caller attempts the real
// reservation); role is the same clustering.MemberRole Decide was given;
// mode is sourcepolicy.Resolver.Mode() at the time of the attempt;
// acquired is the result of the caller's atomic reservation attempt
// (out.SourcePolicyCooldownRepository.Reserve for a non-correction role,
// or always true/never checked when the caller correctly used
// ForceReserve for MemberRoleCorrection instead of Reserve — see the
// caller contract in the design doc). Pure, no I/O: this function never
// decides whether to call Reserve or ForceReserve itself, it only
// interprets the result the caller already obtained.
func ResolveCooldownReservation(decision Decision, role clustering.MemberRole, mode Mode, acquired bool) (Decision, ReservationOutcome) {
	if !IsCooldownRelevant(decision.Code) || !decision.ShouldPublish {
		return decision, OutcomeNotApplicable
	}
	if mode != ModeEnforce {
		return decision, OutcomeShadow
	}
	if role == clustering.MemberRoleCorrection {
		return decision, OutcomeForced
	}
	if acquired {
		return decision, OutcomeReserved
	}
	blocked := decision
	blocked.Code = CodeRejectCooldown
	blocked.ShouldPublish = false
	blocked.Explanation = "lost the cooldown reservation to a concurrent candidate for the same source and cluster"
	return blocked, OutcomeBlocked
}
