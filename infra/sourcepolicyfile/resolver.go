// infra/sourcepolicyfile/resolver.go
package sourcepolicyfile

import (
	"strconv"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

// Resolver implements sourcepolicy.Resolver over an in-memory File loaded
// by Load. A nil File (e.g. main.go chose not to load one) makes every
// call fully inert — ModeDisabled, DefaultPolicy for every source.
type Resolver struct {
	byID    map[string]PolicyEntry
	mode    sourcepolicy.Mode
	version string
}

// NewResolver builds a Resolver over f. f may be nil.
func NewResolver(f *File) *Resolver {
	if f == nil {
		return &Resolver{byID: map[string]PolicyEntry{}, mode: sourcepolicy.ModeDisabled}
	}
	byID := make(map[string]PolicyEntry, len(f.Policies))
	for _, p := range f.Policies {
		byID[p.SourceID.String()] = p
	}
	return &Resolver{byID: byID, mode: sourcepolicy.Mode(f.Mode), version: strconv.Itoa(f.Version)}
}

// Resolve implements sourcepolicy.Resolver.
func (r *Resolver) Resolve(sourceID string) sourcepolicy.Policy {
	entry, ok := r.byID[sourceID]
	if !ok {
		return sourcepolicy.DefaultPolicy(sourceID)
	}
	return sourcepolicy.Policy{
		SourceID:                                 sourceID,
		Version:                                  r.version,
		Authority:                                sourcepolicy.AuthorityLevel(entry.Authority),
		RetainNearDuplicates:                     entry.RetainNearDuplicates,
		RepresentativePreference:                 sourcepolicy.RepresentativePreference(entry.RepresentativePreference),
		AllowOfficialConfirmation:                entry.AllowOfficialConfirmation,
		AllowSubstantialUpdate:                   entry.AllowSubstantialUpdate,
		PublishConfirmationWithoutNewInformation: entry.PublishConfirmationWithoutNewInformation,
		MinimumUpdateNovelty:                     entry.MinimumUpdateNovelty,
		EventCooldown:                            entry.EventCooldownDuration(),
	}
}

// Mode implements sourcepolicy.Resolver.
func (r *Resolver) Mode() sourcepolicy.Mode {
	return r.mode
}
