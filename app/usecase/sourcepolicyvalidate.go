package usecase

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

// SourcePolicyValidation is the result of ValidateSourcePolicies.
type SourcePolicyValidation struct {
	Issues []string
}

// ValidateSourcePolicies cross-checks an already-loaded source-policy file
// against an already-loaded source catalog: every policy's source_id must
// match exactly one configured source (an orphaned policy — no match — is
// an issue; sources.Validate() already rejects two sources resolving to
// the same ID, so "more than one match" cannot happen here). Structural
// validation (malformed YAML, an unparseable source_id, an invalid
// per-platform format) has already happened in sourcesfile.Load and
// sourcepolicyfile.Load by the time either argument reaches here — those
// return a hard error, not a soft Issues entry.
func ValidateSourcePolicies(sources *sourcesfile.Sources, policyFile *sourcepolicyfile.File) SourcePolicyValidation {
	known := make(map[string]bool)
	ids, err := sources.IDs()
	if err == nil { // Load() already validated this; err == nil is expected.
		for _, id := range ids {
			known[id.String()] = true
		}
	}

	var issues []string
	for _, p := range policyFile.Policies {
		if !known[p.SourceID.String()] {
			issues = append(issues, fmt.Sprintf("policy for source_id %q has no matching entry in sources.yml", p.SourceID))
		}
	}
	return SourcePolicyValidation{Issues: issues}
}
