// domain/eventruleproposal/normalize_test.go
package eventruleproposal

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

var normalizeBase = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// normalizeFixture returns the stored type proposals of the normalization
// tests, created one hour apart in the listed order.
func normalizeFixture() []Proposal {
	row := func(i int, name string, st Status, target string, evidence ...string) Proposal {
		p := Proposal{
			ID: "evr_" + string(rune('a'+i)), Kind: KindType, TypeName: name, Target: target,
			Description: name + ".", EvidenceIDs: evidence, Status: st, PromptVersion: "v2",
			CreatedAt: normalizeBase.Add(time.Duration(i) * time.Hour),
		}
		if st != StatusPending {
			p.DecidedAt = p.CreatedAt.Add(30 * time.Minute)
		}
		return p
	}
	return []Proposal{
		row(0, "player injury status", StatusApproved, "Injury-Reserve Move", "e1"),
		row(1, "player injury status", StatusApproved, "", "e2"),
		row(2, "player_injury_status", StatusApproved, "", "e3"),
		row(3, "game result", StatusApproved, "", "e4"),
		row(4, "kicking error", StatusPending, "", "e5"),
		row(5, "logo change", StatusRejected, ""),
		row(6, "///", StatusPending, ""),
	}
}

var normalizeKnown = map[string]bool{"game-result": true}

func survivors(plan NormalizePlan) map[string]Proposal {
	out := map[string]Proposal{}
	for _, p := range plan.Insert {
		out[p.TypeName] = p
	}
	return out
}

func TestPlanTypeNormalization(t *testing.T) {
	now := normalizeBase.Add(48 * time.Hour)
	ps := normalizeFixture()
	plan := PlanTypeNormalization(ps, normalizeKnown, nil, true, now)

	deleted := append([]string(nil), plan.Delete...)
	sort.Strings(deleted)
	if want := []string{"evr_a", "evr_b", "evr_c", "evr_d", "evr_e", "evr_f", "evr_g"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("Delete = %v, want %v", deleted, want)
	}
	if plan.Merged != 1 || plan.Covered != 1 || plan.Reopened != 1 || plan.Emptied != 1 {
		t.Fatalf("counts = merged %d covered %d reopened %d emptied %d, want 1 1 1 1", plan.Merged, plan.Covered, plan.Reopened, plan.Emptied)
	}
	got := survivors(plan)
	if len(got) != 4 || len(plan.Insert) != 4 {
		t.Fatalf("Insert = %+v, want 4 survivors", plan.Insert)
	}

	pis := got["player-injury-status"]
	if pis.Status != StatusPending || !pis.DecidedAt.IsZero() {
		t.Errorf("player-injury-status = %s decided %v, want pending undecided", pis.Status, pis.DecidedAt)
	}
	if !reflect.DeepEqual(pis.EvidenceIDs, []string{"e1", "e2", "e3"}) {
		t.Errorf("player-injury-status evidence = %v, want [e1 e2 e3]", pis.EvidenceIDs)
	}
	if pis.Target != "Injury-Reserve Move" || !pis.CreatedAt.Equal(normalizeBase) || pis.Kind != KindType ||
		pis.Description != "player injury status." || pis.PromptVersion != "v2" {
		t.Errorf("player-injury-status = %+v, want earliest target, created_at, description and prompt version", pis)
	}

	if gr := got["game-result"]; gr.Status != StatusRejected || !gr.DecidedAt.Equal(now) {
		t.Errorf("game-result = %s decided %v, want rejected at now", gr.Status, gr.DecidedAt)
	}
	if ke := got["kicking-error"]; ke.Status != StatusPending || !ke.DecidedAt.IsZero() {
		t.Errorf("kicking-error = %s decided %v, want pending", ke.Status, ke.DecidedAt)
	}
	if lc := got["logo-change"]; lc.Status != StatusRejected || !lc.DecidedAt.Equal(ps[5].DecidedAt) {
		t.Errorf("logo-change = %s decided %v, want rejected keeping %v", lc.Status, lc.DecidedAt, ps[5].DecidedAt)
	}
}

func TestPlanTypeNormalization_KeepsApprovedWithoutReopen(t *testing.T) {
	plan := PlanTypeNormalization(normalizeFixture(), normalizeKnown, nil, false, normalizeBase.Add(48*time.Hour))
	if pis := survivors(plan)["player-injury-status"]; pis.Status != StatusApproved {
		t.Fatalf("player-injury-status = %s, want approved", pis.Status)
	}
	if plan.Reopened != 0 {
		t.Fatalf("Reopened = %d, want 0", plan.Reopened)
	}
}

func TestPlanTypeNormalization_Idempotent(t *testing.T) {
	for _, reopen := range []bool{true, false} {
		now := normalizeBase.Add(48 * time.Hour)
		ps := normalizeFixture()
		plan := PlanTypeNormalization(ps, normalizeKnown, nil, reopen, now)

		deleted := map[string]bool{}
		for _, id := range plan.Delete {
			deleted[id] = true
		}
		var after []Proposal
		for _, p := range ps {
			if !deleted[p.ID] {
				after = append(after, p)
			}
		}
		for _, p := range plan.Insert {
			p.ID = "evr_" + p.TypeName
			after = append(after, p)
		}

		again := PlanTypeNormalization(after, normalizeKnown, nil, reopen, now.Add(time.Hour))
		if len(again.Delete) != 0 || len(again.Insert) != 0 {
			t.Fatalf("reopen=%v: second plan = %+v, want nothing to do", reopen, again)
		}
	}
}

func TestPlanTypeNormalization_SkipsAppliedSlugs(t *testing.T) {
	now := normalizeBase.Add(48 * time.Hour)
	ps := []Proposal{
		{ID: "evr_x", Kind: KindType, TypeName: "kicking error", Status: StatusPending, CreatedAt: normalizeBase, EvidenceIDs: []string{"e1"}},
		{ID: "evr_y", Kind: KindType, TypeName: "kicking_error", Status: StatusApproved, CreatedAt: normalizeBase.Add(time.Hour), DecidedAt: normalizeBase.Add(2 * time.Hour)},
	}
	plan := PlanTypeNormalization(ps, nil, map[string]bool{"kicking-error": true}, false, now)
	if !reflect.DeepEqual(plan.Delete, []string{"evr_x", "evr_y"}) || len(plan.Insert) != 0 || plan.Covered != 1 || plan.Merged != 0 {
		t.Fatalf("plan = %+v, want both rows deleted, nothing inserted, Covered 1", plan)
	}
}

func TestPlanTypeNormalization_KeepsApprovalChoices(t *testing.T) {
	now := normalizeBase.Add(48 * time.Hour)
	ps := []Proposal{
		{ID: "evr_x", Kind: KindType, TypeName: "kicking error", Status: StatusPending, CreatedAt: normalizeBase},
		{ID: "evr_y", Kind: KindType, TypeName: "kicking_error", Status: StatusApproved, CreatedAt: normalizeBase.Add(time.Hour), DecidedAt: normalizeBase.Add(2 * time.Hour), TypeWindow: "72h", TypeReferential: true},
	}
	plan := PlanTypeNormalization(ps, nil, nil, false, now)
	got := survivors(plan)["kicking-error"]
	if got.Status != StatusApproved || got.TypeWindow != "72h" || !got.TypeReferential {
		t.Fatalf("survivor = %+v, want approved with window 72h and referential", got)
	}
}
