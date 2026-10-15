// Package editoriallabelproposal models offline, review-gated label
// suggestions produced by the LLM "teacher" for the
// active-learning workflow. A Proposal is always independently derived
// from article content alone -- it is never shown the
// production Naive Bayes classifier's own prediction, so that agreement
// between the two is never mere confirmation of a prediction the LLM
// already saw.
package editoriallabelproposal

import (
	"errors"
	"time"
)

// ErrNotFound is returned when a proposal id does not exist.
var ErrNotFound = errors.New("editoriallabelproposal: not found")

// ErrPendingProposalExists is returned by Save when the target article
// already has a pending proposal -- mirrors idx_editorial_label_
// proposals_pending_identity. Callers must resolve (approve/reject) the
// existing proposal before a new one can be created for the same
// article.
var ErrPendingProposalExists = errors.New("editoriallabelproposal: article already has a pending proposal")

// Status mirrors classifierproposal.Status / entityproposal's status
// values for the same pending/approved/rejected review lifecycle.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// Proposal is one LLM classification of one article: an independent
// publish/discard/uncertain suggestion, its confidence, and the audit
// trail needed to later explain or distrust it.
type Proposal struct {
	ID            string
	ArticleID     string
	Label         string // "publish" | "discard" | "uncertain"
	Confidence    float64
	Reason        string
	Model         string
	PromptVersion string
	PolicyVersion string
	Status        Status
	CreatedAt     time.Time
	DecidedAt     time.Time
}

// Tier is a training dataset's quality tier. Only
// Gold and approved Silver examples should be used to train a candidate
// classifier.
type Tier string

const (
	// TierGold examples have a human-confirmed label: manual, or a
	// human review of an LLM proposal (both recorded as a
	// human_labels/editorial_examples row -- see HumanLabelRepository).
	TierGold Tier = "gold"
	// TierSilver examples have no human label yet but a sufficiently
	// confident, unreviewed (or reviewed-and-agreed) LLM proposal.
	TierSilver Tier = "silver"
	// TierPending examples have neither a human label nor a
	// sufficiently confident LLM proposal -- candidates for active-
	// learning review, not yet trainable.
	TierPending Tier = "pending"
)

// ComputeTier deterministically assigns a Tier from already-known facts
// about one article: whether it has an active human label, and its most
// recent LLM proposal (nil if none exists yet). The LLM itself never
// computes this.
//
// A human label always wins regardless of what any proposal says (issue
// #228 part 3): manual decisions are never downgraded or reconsidered
// here.
func ComputeTier(hasActiveHumanLabel bool, latestProposal *Proposal, autoLabelThreshold float64) Tier {
	if hasActiveHumanLabel {
		return TierGold
	}
	if latestProposal != nil && latestProposal.Confidence >= autoLabelThreshold {
		return TierSilver
	}
	return TierPending
}
