// Package entityproposal models a candidate addition/alias to the
// deterministic entity catalog (domain/eventsignals), mined by
// the offline `entities discover` command from stored article title/
// summary text via an LLM (see app/entitydiscovery).
// This package has no I/O and no LLM dependency of its own — it only
// defines the shape of a proposal and the pure comparison helpers
// (Normalize/Surname/Score) the application layer uses to aggregate and
// rank proposals.
package entityproposal

import (
	"errors"
	"strings"
	"time"
)

// ErrNotFound is the domain-level "no such proposal" sentinel —
// repository adapters wrap it alongside their own persistence-specific
// not-found error, same pattern as article.ErrNotFound.
var ErrNotFound = errors.New("entityproposal: not found")

// ErrConflict means a write would make a proposal collide with another
// existing one (same normalized name and entity type among pending rows).
var ErrConflict = errors.New("entityproposal: conflicts with an existing proposal")

// ReasonAutoRejectedNonNFL is the DecisionReason recorded when entity
// verification rejects a candidate automatically because it is not an NFL
// entity. Upsert never re-queues such a row as pending.
const ReasonAutoRejectedNonNFL = "auto_rejected_non_nfl"

// ReasonDuplicateOfExisting is the DecisionReason recorded when
// verification renames/retypes a pending proposal onto an identity that
// another pending proposal already holds. Upsert never re-queues such a
// row as pending.
const ReasonDuplicateOfExisting = "duplicate_of_existing"

// ReasonNotAnNFLTeam is the DecisionReason recorded when a team proposal
// matches none of the 32 NFL teams' aliases (closed set). Upsert never
// re-queues such a row as pending.
const ReasonNotAnNFLTeam = "not_an_nfl_team"

// ReasonAutoRejectedNoise is the DecisionReason recorded when a proposal fails
// the deterministic noise filter (handle, URL, digits, too few letters).
// Upsert never re-queues such a row as pending.
const ReasonAutoRejectedNoise = "auto_rejected_noise"

// EntityType is the kind of real-world thing a Proposal names. Values
// match domain/eventsignals.EntityKind's string values exactly
// (player/coach/executive/team) so a Proposal's EntityType casts
// directly to an eventsignals.EntityKind without a translation table.
type EntityType string

const (
	EntityTypePlayer    EntityType = "player"
	EntityTypeCoach     EntityType = "coach"
	EntityTypeExecutive EntityType = "executive"
	EntityTypeTeam      EntityType = "team"
)

// Kind classifies what a Proposal represents relative to the catalog
// already loaded when it was created.
type Kind string

const (
	// KindNewEntity: no catalog entity matched this name at all.
	KindNewEntity Kind = "new_entity"
	// KindNewAlias: TargetEntityID names an existing catalog entity this
	// proposal's Aliases should be added to.
	KindNewAlias Kind = "new_alias"
	// KindPossibleDuplicate: this name shares a surname with exactly one
	// other brand-new candidate discovered in the same run, and neither
	// was merged automatically — a human must say --merge-into or
	// --as-new.
	KindPossibleDuplicate Kind = "possible_duplicate"
)

// Status is a Proposal's review state.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

// ProposalSource records which pipeline produced a Proposal: the offline
// batch-mining `entities discover` command, or the online,
// per-candidate fallback that runs inside ingestion.
type ProposalSource string

const (
	ProposalSourceDiscovery      ProposalSource = "discovery"
	ProposalSourceOnlineFallback ProposalSource = "online_fallback"
)

// Source is one article that provided evidence for a Proposal.
type Source struct {
	ArticleID    string
	ArticleTitle string
}

// WebSource is one trusted-domain web-search result used as evidence for
// a Proposal. Unlike Source (an originating stored article),
// a WebSource is external corroboration the offline `entities discover
// --verify` tool loop retrieved and the application judged trustworthy —
// an untrusted-domain result is dropped before it ever reaches a
// Proposal (see app/catalogverification).
type WebSource struct {
	Domain string
	URL    string
	Title  string
}

// Verification records which verifier established a proposal's identity.
// Empty means never externally verified.
type Verification struct {
	Provider   string // "wikidata", "llm_web"
	ProviderID string // e.g. a Wikidata QID; empty for web verification
	Version    string // verifier/scorer version, independent of ExtractorVersion
}

// Proposal is one candidate catalog change, aggregated across every
// article and every `entities discover` run that observed it.
type Proposal struct {
	ID             string
	CanonicalName  string
	NormalizedName string
	EntityType     EntityType
	Kind           Kind
	TargetEntityID string // set only when Kind == KindNewAlias
	Metadata       map[string]string
	Confidence     float64
	Occurrences    int
	Aliases        []string
	Sources        []Source
	WebSources     []WebSource
	Status         Status
	// DecisionReason records why a non-pending status was set (empty for
	// manual decisions); see ReasonAutoRejectedNonNFL.
	DecisionReason string
	// Verification is the provenance of the verifier that decided or
	// enriched this proposal.
	Verification Verification
	// Source is "discovery" for every proposal created before the online
	// fallback shipped (backfilled by migration 0033's column default) and
	// for every one entitydiscovery.Discover creates today; "online_fallback"
	// for one the online resolution fallback created. When a discovery-run
	// occurrence merges into an existing online_fallback proposal (or vice
	// versa), Source always ends up "discovery" — see
	// sqliteentityproposal.Upsert's merge branch.
	Source           ProposalSource
	FirstSeen        time.Time
	LastSeen         time.Time
	ExtractorVersion string
	CreatedAt        time.Time
	DecidedAt        time.Time
}

// Score ranks p for `entities list`: occurrences, weighted title hits,
// weighted unique sources, plus raw LLM confidence. titleOccurrences and
// uniqueSources are derived from p.Sources here rather than stored
// separately — see the design spec's §6 note that, in this version,
// uniqueSources coincides with Occurrences in practice (both count
// distinct source articles) because cross-source-domain tracking isn't
// implemented yet.
func Score(p Proposal) float64 {
	normName := Normalize(p.CanonicalName)
	titleOccurrences := 0
	for _, s := range p.Sources {
		if normName != "" && strings.Contains(Normalize(s.ArticleTitle), normName) {
			titleOccurrences++
		}
	}
	uniqueSources := len(p.Sources)
	return float64(p.Occurrences) + 1.5*float64(titleOccurrences) + 1.5*float64(uniqueSources) + p.Confidence
}
