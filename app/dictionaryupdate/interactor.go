package dictionaryupdate

import "context"

type Action string

const (
	ActionAccept           Action = "accept"
	ActionReject           Action = "reject"
	ActionEdit             Action = "edit"
	ActionSkip             Action = "skip"
	ActionAcceptAllOfType  Action = "accept_all_of_type"
	ActionRejectAllOfType  Action = "reject_all_of_type"
	ActionViewSource       Action = "view_source"
	ActionViewCurrentEntry Action = "view_current_entry"
	ActionViewConflicts    Action = "view_conflicts"
	ActionBack             Action = "back"
	ActionQuitAndSave      Action = "quit_and_save"
	ActionAbort            Action = "abort"
	ActionHelp             Action = "help"
)

type ReviewState struct {
	Index int
	Total int
}

// ReviewPort is the seam between the pure review loop and any reviewer
// surface (a real terminal, a scripted test double, ...). Implementations
// decide how to prompt and how to parse a response into an Action, but
// never decide what happens as a result of that action. Edited is
// non-nil only when Action is ActionEdit.
type ReviewPort interface {
	Next(ctx context.Context, proposal Proposal, state ReviewState) (Action, *Proposal, error)
}
