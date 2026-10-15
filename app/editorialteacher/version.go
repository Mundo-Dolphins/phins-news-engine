// Package editorialteacher classifies articles against the publication
// policy using an offline LLM "teacher" model.
package editorialteacher

import _ "embed"

// PromptVersion tags every proposal and processed-article record this
// package's classifier produces. Bump it in the same PR that changes
// the prompt-building logic (prompt.go) or the output-schema
// instructions in infra/llmclient's Classify, so PromptVersion always
// identifies exactly what prompt shape produced a given proposal.
const PromptVersion = "teacher-prompt-v2"

// DefaultPolicyVersion identifies the library's embedded, team-neutral
// policy (policy/publication-policy.md). Bump it whenever that file's
// content changes meaningfully.
const DefaultPolicyVersion = "generic-publication-policy-v1"

//go:embed policy/publication-policy.md
var defaultPublicationPolicy string

// PolicyVersion tags every proposal with which publication policy text
// it was judged against. Host programs that supply their own policy set
// it together with PublicationPolicy through SetPublicationPolicy.
var PolicyVersion = DefaultPolicyVersion

// PublicationPolicy is the policy text the teacher model judges articles
// against. It defaults to the library's team-neutral policy.
var PublicationPolicy = defaultPublicationPolicy

// SetPublicationPolicy replaces the policy text and its version tag. A
// host program calls it once at startup, before any classification; an
// empty text (or version) restores the library default for that value.
func SetPublicationPolicy(version, text string) {
	if version == "" {
		version = DefaultPolicyVersion
	}
	if text == "" {
		text = defaultPublicationPolicy
	}
	PolicyVersion, PublicationPolicy = version, text
}
