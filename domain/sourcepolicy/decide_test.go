// domain/sourcepolicy/decide_test.go
package sourcepolicy_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

// TestDecide_DefaultPolicy_NonDuplicateRolesAlwaysPublishStandard covers
// the roles that were never a duplicate/near-duplicate situation to begin
// with — clustering.DecideJoin's own "not a policy-relevant duplication
// signal" cases. These are unaffected by removing the IsUnconfigured()
// short-circuit: they hit the switch's first case unconditionally.
func TestDecide_DefaultPolicy_NonDuplicateRolesAlwaysPublishStandard(t *testing.T) {
	now := time.Now()
	roles := []clustering.MemberRole{
		clustering.MemberRoleFirstReport, clustering.MemberRoleAnalysis,
		clustering.MemberRoleReaction, clustering.MemberRolePrimarySource, clustering.MemberRoleRelatedCoverage,
	}
	for _, role := range roles {
		d := sourcepolicy.Decide(role, sourcepolicy.DefaultPolicy("x"), 0, now)
		if d.Code != sourcepolicy.CodeStandard {
			t.Errorf("Decide(%q, DefaultPolicy) = %q, want %q", role, d.Code, sourcepolicy.CodeStandard)
		}
		if !d.ShouldPublish {
			t.Errorf("Decide(%q, DefaultPolicy).ShouldPublish = false, want true", role)
		}
	}
}

// TestDecide_DefaultPolicy_DuplicateRolesDoNotPublish is the regression
// test for the root cause: an unconfigured source (DefaultPolicy)
// must NOT be allowed to publish a confirmation/substantial-update/
// correction — those require an explicit allow_* flag, matching
// configs/source-policies.yml's own header comment ("no entry ⇒ no
// confirmation/update publication"). Before this fix, Decide's
// IsUnconfigured() short-circuit made every one of these ShouldPublish=true
// regardless of role.
func TestDecide_DefaultPolicy_DuplicateRolesDoNotPublish(t *testing.T) {
	now := time.Now()
	tests := []struct {
		role     clustering.MemberRole
		wantCode sourcepolicy.DecisionCode
	}{
		{clustering.MemberRoleConfirmation, sourcepolicy.CodeRetainClusterMember},
		{clustering.MemberRoleSubstantialUpdate, sourcepolicy.CodeRejectInsufficientNovelty},
		{clustering.MemberRoleCorrection, sourcepolicy.CodeRejectInsufficientNovelty},
	}
	for _, tt := range tests {
		d := sourcepolicy.Decide(tt.role, sourcepolicy.DefaultPolicy("x"), 0, now)
		if d.Code != tt.wantCode {
			t.Errorf("Decide(%q, DefaultPolicy).Code = %q, want %q", tt.role, d.Code, tt.wantCode)
		}
		if d.ShouldPublish {
			t.Errorf("Decide(%q, DefaultPolicy).ShouldPublish = true, want false: an unconfigured source must never publish a %s", tt.role, tt.role)
		}
	}
}

// TestDecide_DefaultPolicy_PlainDuplicateStaysCodeStandard documents that
// MemberRoleDuplicate is the one exception left at CodeStandard/
// ShouldPublish=true for an unconfigured policy — Decide's own comment
// explains why ("standard rejection applies upstream": Gate A only ever
// assigns this role when RetainNearDuplicates is already true, so an
// unconfigured source can never actually reach this branch in practice).
// The new cluster-publication gate (domain/clustergate) is the
// mode-independent backstop that denies this case regardless, should that
// upstream assumption ever be violated.
func TestDecide_DefaultPolicy_PlainDuplicateStaysCodeStandard(t *testing.T) {
	d := sourcepolicy.Decide(clustering.MemberRoleDuplicate, sourcepolicy.DefaultPolicy("x"), 0, time.Now())
	if d.Code != sourcepolicy.CodeStandard || !d.ShouldPublish {
		t.Errorf("Decide(Duplicate, DefaultPolicy) = %+v, want Code=CodeStandard ShouldPublish=true", d)
	}
}

func TestDecide_ConfirmationWithoutNewInfo_NotPublishedByDefault(t *testing.T) {
	policy := sourcepolicy.Policy{
		SourceID: "sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam,
		AllowOfficialConfirmation: true, PublishConfirmationWithoutNewInformation: false,
	}
	d := sourcepolicy.Decide(clustering.MemberRoleConfirmation, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodeOfficialConfirmationNoPublish {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodeOfficialConfirmationNoPublish)
	}
	if d.ShouldPublish {
		t.Error("ShouldPublish = true, want false")
	}
	if d.Novelty != sourcepolicy.NoveltyConfirmation {
		t.Errorf("Novelty = %q, want %q", d.Novelty, sourcepolicy.NoveltyConfirmation)
	}
}

func TestDecide_ConfirmationWithoutNewInfo_PublishedWhenConfigured(t *testing.T) {
	policy := sourcepolicy.Policy{
		SourceID: "sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam,
		AllowOfficialConfirmation: true, PublishConfirmationWithoutNewInformation: true,
	}
	d := sourcepolicy.Decide(clustering.MemberRoleConfirmation, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodePublishOfficialConfirmation {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodePublishOfficialConfirmation)
	}
	if !d.ShouldPublish {
		t.Error("ShouldPublish = false, want true")
	}
}

func TestDecide_ConfirmationNotAllowed_RetainedOnly(t *testing.T) {
	policy := sourcepolicy.Policy{SourceID: "x", AllowOfficialConfirmation: false, RetainNearDuplicates: true}
	d := sourcepolicy.Decide(clustering.MemberRoleConfirmation, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodeRetainClusterMember {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodeRetainClusterMember)
	}
	if d.ShouldPublish {
		t.Error("ShouldPublish = true, want false")
	}
}

func TestDecide_SubstantialUpdate_PublishedWhenNoveltyClearsThresholdAndAllowed(t *testing.T) {
	policy := sourcepolicy.Policy{
		SourceID: "sprisotopes", AllowSubstantialUpdate: true, MinimumUpdateNovelty: 0.5,
	}
	d := sourcepolicy.Decide(clustering.MemberRoleSubstantialUpdate, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodePublishSubstantialUpdate {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodePublishSubstantialUpdate)
	}
	if !d.ShouldPublish {
		t.Error("ShouldPublish = false, want true")
	}
}

func TestDecide_SubstantialUpdate_NotAllowed_Rejected(t *testing.T) {
	policy := sourcepolicy.Policy{SourceID: "x", AllowSubstantialUpdate: false}
	d := sourcepolicy.Decide(clustering.MemberRoleSubstantialUpdate, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodeRejectInsufficientNovelty {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodeRejectInsufficientNovelty)
	}
	if d.ShouldPublish {
		t.Error("ShouldPublish = true, want false")
	}
}

func TestDecide_Cooldown_BlocksPublicationEvenWhenAllowed(t *testing.T) {
	policy := sourcepolicy.Policy{
		SourceID: "x", AllowSubstantialUpdate: true, EventCooldown: 20 * time.Minute,
	}
	d := sourcepolicy.Decide(clustering.MemberRoleSubstantialUpdate, policy, 5*time.Minute, time.Now())
	if d.Code != sourcepolicy.CodeRejectCooldown {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodeRejectCooldown)
	}
	if d.ShouldPublish {
		t.Error("ShouldPublish = true, want false")
	}
}

func TestDecide_Correction_BypassesCooldownWhenAllowed(t *testing.T) {
	// Corrections are never source-gated duplicates (issue's "official
	// correction" test case): a correction is allowed to publish within
	// cooldown as long as the policy allows substantial updates at all —
	// factual correctness takes priority over pacing.
	policy := sourcepolicy.Policy{
		SourceID: "x", AllowSubstantialUpdate: true, EventCooldown: 20 * time.Minute,
	}
	d := sourcepolicy.Decide(clustering.MemberRoleCorrection, policy, 5*time.Minute, time.Now())
	if d.Code != sourcepolicy.CodePublishSubstantialUpdate {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodePublishSubstantialUpdate)
	}
	if !d.ShouldPublish {
		t.Error("ShouldPublish = false, want true")
	}
	if d.Novelty != sourcepolicy.NoveltyCorrection {
		t.Errorf("Novelty = %q, want %q", d.Novelty, sourcepolicy.NoveltyCorrection)
	}
}

func TestDecide_PlainDuplicate_RetainedWhenPolicyAllowsRetention(t *testing.T) {
	policy := sourcepolicy.Policy{SourceID: "x", RetainNearDuplicates: true}
	d := sourcepolicy.Decide(clustering.MemberRoleDuplicate, policy, 0, time.Now())
	if d.Code != sourcepolicy.CodeRetainClusterMember {
		t.Errorf("Code = %q, want %q", d.Code, sourcepolicy.CodeRetainClusterMember)
	}
	if d.ShouldPublish {
		t.Error("ShouldPublish = true, want false")
	}
}
