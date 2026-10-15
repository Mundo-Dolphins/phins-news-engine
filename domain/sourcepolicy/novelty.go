// domain/sourcepolicy/novelty.go
package sourcepolicy

import "github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"

// NoveltyLevel classifies how much new information a cluster member
// contributes relative to what's already been published about the same
// event. Derived entirely from clustering's own already-computed
// MemberRole (itself derived from eventsignals.Representation deltas) —
// never from lexical/text-similarity diffing, per this phase's design.
type NoveltyLevel string

const (
	NoveltyNone         NoveltyLevel = "none"
	NoveltyMinor        NoveltyLevel = "minor"
	NoveltyConfirmation NoveltyLevel = "confirmation"
	NoveltySubstantial  NoveltyLevel = "substantial"
	NoveltyCorrection   NoveltyLevel = "correction"
)

// ClassifyNovelty maps a clustering.MemberRole (already decided by
// clustering.DecideJoin before this is ever called) to a NoveltyLevel.
// MemberRoleFirstReport/Analysis/Reaction/PrimarySource have no
// "is this an update" meaning and map to NoveltyMinor, not because they
// are minor updates but because the question doesn't apply to them.
func ClassifyNovelty(role clustering.MemberRole) NoveltyLevel {
	switch role {
	case clustering.MemberRoleDuplicate:
		return NoveltyNone
	case clustering.MemberRoleConfirmation:
		return NoveltyConfirmation
	case clustering.MemberRoleSubstantialUpdate:
		return NoveltySubstantial
	case clustering.MemberRoleCorrection:
		return NoveltyCorrection
	default:
		return NoveltyMinor
	}
}
