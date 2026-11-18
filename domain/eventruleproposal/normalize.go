// domain/eventruleproposal/normalize.go
package eventruleproposal

import (
	"sort"
	"time"
)

// NormalizePlan is the rewrite that folds stored type proposals onto their
// slugs: the ids in Delete are removed and the proposals in Insert (one per
// slug, ID left for the store to assign) are added. The counters say what
// happened: Merged groups of two or more rows, Covered groups whose slug is
// a known name, Reopened groups whose approval was reset to pending, Emptied
// groups whose name has no slug.
type NormalizePlan struct {
	Delete                             []string
	Insert                             []Proposal
	Merged, Covered, Reopened, Emptied int
}

// PlanTypeNormalization plans the normalization of ps, every stored type
// proposal that is not applied. Rows are grouped by Slug(TypeName); each
// group becomes one survivor named by the slug, keeping the earliest
// CreatedAt, the earliest non-empty Description, Target and PromptVersion,
// and the union of the evidence in CreatedAt order. Its status is rejected
// at now when the slug is in known; otherwise pending or approved when any
// member is (approved only if one was approved and !reopenApproved);
// otherwise rejected at the latest member's DecidedAt. A group with no slug
// is deleted. A group whose slug is in applied (already in the generated
// rules file) is deleted whole and counted as covered: the applied row stays
// the one record of that type. A single row already named by its slug whose status would not
// change is left alone, so a second run plans nothing.
func PlanTypeNormalization(ps []Proposal, known, applied map[string]bool, reopenApproved bool, now time.Time) NormalizePlan {
	sorted := append([]Proposal(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	var order []string
	groups := map[string][]Proposal{}
	for _, p := range sorted {
		s := Slug(p.TypeName)
		if _, ok := groups[s]; !ok {
			order = append(order, s)
		}
		groups[s] = append(groups[s], p)
	}

	var plan NormalizePlan
	for _, s := range order {
		members := groups[s]
		if s == "" {
			for _, m := range members {
				plan.Delete = append(plan.Delete, m.ID)
			}
			plan.Emptied++
			continue
		}
		if applied[s] {
			for _, m := range members {
				plan.Delete = append(plan.Delete, m.ID)
			}
			plan.Covered++
			continue
		}
		survivor, covered, reopened := fold(s, members, known[s], reopenApproved, now)
		if len(members) == 1 && members[0].TypeName == s && members[0].Status == survivor.Status {
			continue
		}
		for _, m := range members {
			plan.Delete = append(plan.Delete, m.ID)
		}
		plan.Insert = append(plan.Insert, survivor)
		if len(members) > 1 {
			plan.Merged++
		}
		if covered {
			plan.Covered++
		}
		if reopened {
			plan.Reopened++
		}
	}
	return plan
}

// fold builds the survivor of members (sorted by CreatedAt) named slug, and
// reports whether it is covered by a known name and whether an approval was
// reset to pending.
func fold(slug string, members []Proposal, isKnown, reopenApproved bool, now time.Time) (Proposal, bool, bool) {
	first := members[0]
	p := Proposal{Kind: KindType, TypeName: slug, CreatedAt: first.CreatedAt}
	seen := map[string]bool{}
	var anyOpen, anyApproved bool
	var lastDecided, lastApproved time.Time
	for _, m := range members {
		if p.Description == "" {
			p.Description = m.Description
		}
		if p.Target == "" && m.Target != "" {
			p.Target, p.TargetKind = m.Target, m.TargetKind
		}
		if p.PromptVersion == "" {
			p.PromptVersion = m.PromptVersion
		}
		for _, id := range m.EvidenceIDs {
			if !seen[id] {
				seen[id] = true
				p.EvidenceIDs = append(p.EvidenceIDs, id)
			}
		}
		switch m.Status {
		case StatusApproved:
			anyOpen, anyApproved = true, true
			if m.DecidedAt.After(lastApproved) {
				lastApproved = m.DecidedAt
				p.TypeWindow, p.TypeReferential = m.TypeWindow, m.TypeReferential
			}
		case StatusPending:
			anyOpen = true
		}
		if m.DecidedAt.After(lastDecided) {
			lastDecided = m.DecidedAt
		}
	}
	switch {
	case isKnown:
		p.Status, p.DecidedAt = StatusRejected, now
		return p, true, false
	case anyApproved && !reopenApproved:
		p.Status, p.DecidedAt = StatusApproved, lastApproved
	case anyOpen:
		p.Status = StatusPending
		p.TypeWindow, p.TypeReferential = "", false
		return p, false, anyApproved
	default:
		p.Status, p.DecidedAt = StatusRejected, lastDecided
		p.TypeWindow, p.TypeReferential = "", false
	}
	return p, false, false
}
