// Package sourcepolicy models per-source deduplication and publication
// policy: what an official/priority source is allowed to influence
// downstream of the factual dedup/clustering result (never before it, and
// never for an exact duplicate — see Decide's own doc comment). Pure
// domain package: no YAML, no SQL, no config-file knowledge — those live
// in infra/sourcepolicyfile.
package sourcepolicy

import "time"

const unconfiguredPolicyVersion = "unconfigured"

// AuthorityLevel classifies how authoritative a source is for the events
// it reports on. Configured explicitly per source in
// configs/source-policies.yml — never inferred from account name or text.
type AuthorityLevel string

const (
	AuthorityOfficialTeam    AuthorityLevel = "official_team"
	AuthorityOfficialLeague  AuthorityLevel = "official_league"
	AuthorityOfficialPlayer  AuthorityLevel = "official_player"
	AuthorityOfficialStaff   AuthorityLevel = "official_staff"
	AuthorityPrimaryReporter AuthorityLevel = "primary_reporter"
	AuthorityTrustedReporter AuthorityLevel = "trusted_reporter"
	AuthorityRegularSource   AuthorityLevel = "regular_source"
	AuthorityAggregator      AuthorityLevel = "aggregator"
	// AuthorityUnknown is the safe default for any source with no
	// configured authority — it never receives any special treatment.
	AuthorityUnknown AuthorityLevel = "unknown"
)

// Valid reports whether a is one of the known authority levels.
func (a AuthorityLevel) Valid() bool {
	switch a {
	case AuthorityOfficialTeam, AuthorityOfficialLeague, AuthorityOfficialPlayer, AuthorityOfficialStaff,
		AuthorityPrimaryReporter, AuthorityTrustedReporter, AuthorityRegularSource, AuthorityAggregator, AuthorityUnknown:
		return true
	default:
		return false
	}
}

// RepresentativePreference tunes how strongly a source's authority should
// bias clustering.RepresentativeCandidate.IsPrimarySource. Kept as 4 named
// tiers (not a raw float) so config stays readable; PrefersAsRepresentative
// collapses it to the binary signal clustering.RepresentativeConfig's
// PrimarySourceWeight actually consumes today (see this phase's design
// doc, "Out of scope" — grading this further is deferred).
type RepresentativePreference string

const (
	PreferenceNone   RepresentativePreference = "none"
	PreferenceLow    RepresentativePreference = "low"
	PreferenceMedium RepresentativePreference = "medium"
	PreferenceHigh   RepresentativePreference = "high"
)

// Valid reports whether p is one of the known preference tiers.
func (p RepresentativePreference) Valid() bool {
	switch p {
	case PreferenceNone, PreferenceLow, PreferenceMedium, PreferenceHigh:
		return true
	default:
		return false
	}
}

// Mode is the rollout switch for source-policy behavior, mirroring
// infrastructure/config.EditorialConfig.Mode exactly. Disabled: every
// Resolve call behaves as DefaultPolicy regardless of config content.
// Shadow: policies are resolved and decisions computed/recorded, but never
// change what actually publishes. Enforce: decisions take effect.
type Mode string

const (
	ModeDisabled Mode = "disabled"
	ModeShadow   Mode = "shadow"
	ModeEnforce  Mode = "enforce"
)

// Valid reports whether m is one of the three known modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeDisabled, ModeShadow, ModeEnforce:
		return true
	default:
		return false
	}
}

// Policy is a fully resolved, source-specific policy. The application
// layer only ever sees this type — never raw YAML. There is deliberately
// no field describing exact-duplicate handling: exact-duplicate rejection
// happens entirely before any code path consults a Policy (see the
// design doc's pipeline trace), so there is nothing here for it to
// override even in principle.
type Policy struct {
	SourceID  string
	Version   string
	Authority AuthorityLevel

	RetainNearDuplicates                     bool
	RepresentativePreference                 RepresentativePreference
	AllowOfficialConfirmation                bool
	AllowSubstantialUpdate                   bool
	PublishConfirmationWithoutNewInformation bool
	MinimumUpdateNovelty                     float64
	EventCooldown                            time.Duration
}

// PrefersAsRepresentative reports whether this policy's preference tier is
// strong enough to set clustering.RepresentativeCandidate.IsPrimarySource.
func (p Policy) PrefersAsRepresentative() bool {
	return p.RepresentativePreference == PreferenceMedium || p.RepresentativePreference == PreferenceHigh
}

// DefaultPolicy is the safe policy applied to any source with no
// configured entry, or whenever Mode is ModeDisabled: no retention, no
// confirmation, no update publication, no representative preference —
// today's behavior, unchanged.
func DefaultPolicy(sourceID string) Policy {
	return Policy{
		SourceID:                 sourceID,
		Version:                  unconfiguredPolicyVersion,
		Authority:                AuthorityUnknown,
		RepresentativePreference: PreferenceNone,
	}
}

// IsUnconfigured reports whether this policy is the unconfigured default
// (DefaultPolicy), as opposed to an explicitly configured source policy.
func (p Policy) IsUnconfigured() bool {
	return p.Version == unconfiguredPolicyVersion
}

// Resolver resolves a source.Source.ID to its effective Policy, and
// reports the current global Mode. Implemented in
// infra/sourcepolicyfile; the application layer depends
// only on this interface.
type Resolver interface {
	Resolve(sourceID string) Policy
	Mode() Mode
}
