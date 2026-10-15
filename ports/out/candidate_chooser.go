// ports/out/candidate_chooser.go
package out

import "context"

// ChooseCandidate is one roster player offered to the chooser.
type ChooseCandidate struct{ ID, FullName, Team, Position, Status string }

// ChooseCandidateRequest asks the model to pick the player a short mention
// refers to, from a closed candidate list, using the article text.
type ChooseCandidateRequest struct {
	Mention    string
	Articles   []EntityDiscoveryArticle
	Candidates []ChooseCandidate
}

// ChooseCandidateResult is the model's answer. ChoiceID is "" for an
// abstention; Confidence is "high", "medium" or "low".
type ChooseCandidateResult struct{ ChoiceID, Confidence, Evidence string }

// CandidateChooser is the driven port to the closed-set disambiguation call,
// used only by the offline verification tools.
type CandidateChooser interface {
	ChooseCandidate(ctx context.Context, req ChooseCandidateRequest) (ChooseCandidateResult, error)
}
