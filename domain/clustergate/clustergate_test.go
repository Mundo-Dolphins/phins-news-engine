package clustergate_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustergate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

func TestDecide_AcquiredAlwaysReservesInitialRegardlessOfRoleOrPolicy(t *testing.T) {
	roles := []clustering.MemberRole{
		clustering.MemberRoleFirstReport, clustering.MemberRoleConfirmation,
		clustering.MemberRoleSubstantialUpdate, clustering.MemberRoleCorrection,
		clustering.MemberRoleAnalysis, clustering.MemberRoleReaction,
		clustering.MemberRolePrimarySource, clustering.MemberRoleDuplicate,
	}
	for _, role := range roles {
		if got := clustergate.Decide(true, role, sourcepolicy.CodeStandard); got != clustergate.OutcomeReservedInitial {
			t.Errorf("Decide(true, %q, CodeStandard) = %q, want %q", role, got, clustergate.OutcomeReservedInitial)
		}
	}
}

func TestDecide_NotAcquired_ExemptRolesAlwaysAllowed(t *testing.T) {
	exempt := []clustering.MemberRole{
		clustering.MemberRoleFirstReport, clustering.MemberRoleAnalysis,
		clustering.MemberRoleReaction, clustering.MemberRolePrimarySource,
	}
	for _, role := range exempt {
		if got := clustergate.Decide(false, role, sourcepolicy.CodeStandard); got != clustergate.OutcomeAllowedExempt {
			t.Errorf("Decide(false, %q, CodeStandard) = %q, want %q", role, got, clustergate.OutcomeAllowedExempt)
		}
	}
}

func TestDecide_NotAcquired_NonExemptRoleWithAuthorizedUpdateCode_Allowed(t *testing.T) {
	nonExempt := []clustering.MemberRole{
		clustering.MemberRoleConfirmation, clustering.MemberRoleSubstantialUpdate,
		clustering.MemberRoleCorrection, clustering.MemberRoleDuplicate,
	}
	authorizedCodes := []sourcepolicy.DecisionCode{
		sourcepolicy.CodePublishOfficialConfirmation, sourcepolicy.CodePublishSubstantialUpdate,
	}
	for _, role := range nonExempt {
		for _, code := range authorizedCodes {
			if got := clustergate.Decide(false, role, code); got != clustergate.OutcomeAllowedUpdate {
				t.Errorf("Decide(false, %q, %q) = %q, want %q", role, code, got, clustergate.OutcomeAllowedUpdate)
			}
		}
	}
}

func TestDecide_NotAcquired_NonExemptRoleWithoutAuthorizedCode_Denied(t *testing.T) {
	nonExempt := []clustering.MemberRole{
		clustering.MemberRoleConfirmation, clustering.MemberRoleSubstantialUpdate,
		clustering.MemberRoleCorrection, clustering.MemberRoleDuplicate,
	}
	deniedCodes := []sourcepolicy.DecisionCode{
		sourcepolicy.CodeStandard, sourcepolicy.CodeRetainClusterMember,
		sourcepolicy.CodeOfficialConfirmationNoPublish, sourcepolicy.CodeRejectInsufficientNovelty,
		sourcepolicy.CodeRejectCooldown, sourcepolicy.CodeRequireReview,
	}
	for _, role := range nonExempt {
		for _, code := range deniedCodes {
			if got := clustergate.Decide(false, role, code); got != clustergate.OutcomeDeniedDuplicate {
				t.Errorf("Decide(false, %q, %q) = %q, want %q", role, code, got, clustergate.OutcomeDeniedDuplicate)
			}
		}
	}
}
