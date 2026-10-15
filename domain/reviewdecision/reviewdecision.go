// Package reviewdecision models the actions a human takes during an
// interactive editorial review session and what each one means for the
// training dataset. It has no dependency on storage or CLI.
package reviewdecision

import "fmt"

// Action is one of the choices presented on the review screen.
type Action string

const (
	ActionPublish      Action = "publish"
	ActionReject       Action = "reject"
	ActionAmbiguous    Action = "ambiguous"
	ActionDuplicate    Action = "duplicate"
	ActionSameNews     Action = "same_news"
	ActionUpdate       Action = "update"
	ActionEditCategory Action = "edit_category"
	ActionSkip         Action = "skip"
)

// Valid reports whether a is one of the known actions.
func (a Action) Valid() bool {
	switch a {
	case ActionPublish, ActionReject, ActionAmbiguous, ActionDuplicate, ActionSameNews, ActionUpdate, ActionEditCategory, ActionSkip:
		return true
	default:
		return false
	}
}

// Outcome is what an Action resolves to for the training dataset. A
// zero-value Outcome (Persist: false) means the action carries no
// publish/reject signal — "ambiguous" (deliberately not asserting a
// class), "edit_category" (mutates article metadata, not a label), and
// "skip" all resolve to this.
type Outcome struct {
	Label   string // "publish" | "reject" | ""
	Reason  string
	Persist bool
}

// Resolve maps a to the training Outcome it represents. See the Phase 9a
// design spec's review-action mapping table for the rationale: duplicate/
// same-news/update all correct the *training label*, not live clustering
// state — domain/editorial's classifier stays a single binary model; these are reasons a reject/publish happened, not a second label
// taxonomy.
func Resolve(a Action) (Outcome, error) {
	switch a {
	case ActionPublish:
		return Outcome{Label: "publish", Reason: "", Persist: true}, nil
	case ActionReject:
		return Outcome{Label: "reject", Reason: "quality", Persist: true}, nil
	case ActionDuplicate:
		return Outcome{Label: "reject", Reason: "duplicate", Persist: true}, nil
	case ActionSameNews:
		return Outcome{Label: "reject", Reason: "same_event", Persist: true}, nil
	case ActionUpdate:
		return Outcome{Label: "publish", Reason: "substantial_update", Persist: true}, nil
	case ActionAmbiguous, ActionEditCategory, ActionSkip:
		return Outcome{}, nil
	default:
		return Outcome{}, fmt.Errorf("reviewdecision: unknown action %q", a)
	}
}
