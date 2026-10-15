// domain/sourcepolicy/cooldown_test.go
package sourcepolicy_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

func TestResolveCooldownReservation(t *testing.T) {
	confirmed := sourcepolicy.Decision{Code: sourcepolicy.CodePublishOfficialConfirmation, ShouldPublish: true, MemberRole: clustering.MemberRoleConfirmation}
	update := sourcepolicy.Decision{Code: sourcepolicy.CodePublishSubstantialUpdate, ShouldPublish: true, MemberRole: clustering.MemberRoleSubstantialUpdate}
	correction := sourcepolicy.Decision{Code: sourcepolicy.CodePublishSubstantialUpdate, ShouldPublish: true, MemberRole: clustering.MemberRoleCorrection}
	notApplicable := sourcepolicy.Decision{Code: sourcepolicy.CodeRetainClusterMember, ShouldPublish: false, MemberRole: clustering.MemberRoleConfirmation}
	alreadyCooldown := sourcepolicy.Decision{Code: sourcepolicy.CodeRejectCooldown, ShouldPublish: false, MemberRole: clustering.MemberRoleConfirmation}

	tests := []struct {
		name        string
		decision    sourcepolicy.Decision
		role        clustering.MemberRole
		mode        sourcepolicy.Mode
		acquired    bool
		wantOutcome sourcepolicy.ReservationOutcome
		wantCode    sourcepolicy.DecisionCode
		wantPublish bool
	}{
		{"confirmation enforce acquired", confirmed, clustering.MemberRoleConfirmation, sourcepolicy.ModeEnforce, true, sourcepolicy.OutcomeReserved, sourcepolicy.CodePublishOfficialConfirmation, true},
		{"confirmation enforce lost race", confirmed, clustering.MemberRoleConfirmation, sourcepolicy.ModeEnforce, false, sourcepolicy.OutcomeBlocked, sourcepolicy.CodeRejectCooldown, false},
		{"substantial update enforce acquired", update, clustering.MemberRoleSubstantialUpdate, sourcepolicy.ModeEnforce, true, sourcepolicy.OutcomeReserved, sourcepolicy.CodePublishSubstantialUpdate, true},
		{"substantial update enforce lost race", update, clustering.MemberRoleSubstantialUpdate, sourcepolicy.ModeEnforce, false, sourcepolicy.OutcomeBlocked, sourcepolicy.CodeRejectCooldown, false},
		{"correction always forced regardless of acquired", correction, clustering.MemberRoleCorrection, sourcepolicy.ModeEnforce, false, sourcepolicy.OutcomeForced, sourcepolicy.CodePublishSubstantialUpdate, true},
		{"confirmation shadow mode never attempts", confirmed, clustering.MemberRoleConfirmation, sourcepolicy.ModeShadow, false, sourcepolicy.OutcomeShadow, sourcepolicy.CodePublishOfficialConfirmation, true},
		{"confirmation disabled mode never attempts", confirmed, clustering.MemberRoleConfirmation, sourcepolicy.ModeDisabled, false, sourcepolicy.OutcomeShadow, sourcepolicy.CodePublishOfficialConfirmation, true},
		{"not cooldown-relevant code untouched", notApplicable, clustering.MemberRoleConfirmation, sourcepolicy.ModeEnforce, false, sourcepolicy.OutcomeNotApplicable, sourcepolicy.CodeRetainClusterMember, false},
		{"already-rejected-by-stale-read untouched", alreadyCooldown, clustering.MemberRoleConfirmation, sourcepolicy.ModeEnforce, false, sourcepolicy.OutcomeNotApplicable, sourcepolicy.CodeRejectCooldown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, outcome := sourcepolicy.ResolveCooldownReservation(tt.decision, tt.role, tt.mode, tt.acquired)
			if outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", outcome, tt.wantOutcome)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.ShouldPublish != tt.wantPublish {
				t.Errorf("ShouldPublish = %v, want %v", got.ShouldPublish, tt.wantPublish)
			}
			if got.MemberRole != tt.role {
				t.Errorf("MemberRole = %q, want %q (must be preserved)", got.MemberRole, tt.role)
			}
		})
	}
}
