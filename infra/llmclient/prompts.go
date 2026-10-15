package llmclient

import "sync/atomic"

// Prompts holds the system prompts this package sends that a host program
// may want to word for the team or audience it covers. The library
// defaults (DefaultPrompts) are team-neutral.
type Prompts struct {
	// EntityDiscovery is the system prompt of Client.Discover.
	EntityDiscovery string
	// EntityResolution is the system prompt of Resolver.Resolve.
	EntityResolution string
	// EntityVerification is the system prompt of Client.VerifyCandidate.
	EntityVerification string
	// EventTopics is the system prompt of Client.DiscoverTopics.
	EventTopics string
	// EventPhrases is the system prompt of Client.DiscoverPhrases. It is a
	// fmt format string with exactly two %s verbs: the allowed actions and
	// the allowed signals, in that order.
	EventPhrases string
}

// DefaultPrompts returns the library's team-neutral system prompts.
func DefaultPrompts() Prompts {
	return Prompts{
		EntityDiscovery:    defaultDiscoverySystemPrompt,
		EntityResolution:   defaultResolutionSystemPrompt,
		EntityVerification: defaultVerifySystemPrompt,
		EventTopics:        defaultEventTopicsSystemPrompt,
		EventPhrases:       defaultEventPhrasesSystemPrompt,
	}
}

var currentPrompts atomic.Pointer[Prompts]

// SetPrompts replaces the system prompts used by every Client and Resolver
// from then on. A host program calls it once at startup; an empty field
// keeps that prompt's default.
func SetPrompts(p Prompts) {
	d := DefaultPrompts()
	if p.EntityDiscovery != "" {
		d.EntityDiscovery = p.EntityDiscovery
	}
	if p.EntityResolution != "" {
		d.EntityResolution = p.EntityResolution
	}
	if p.EntityVerification != "" {
		d.EntityVerification = p.EntityVerification
	}
	if p.EventTopics != "" {
		d.EventTopics = p.EventTopics
	}
	if p.EventPhrases != "" {
		d.EventPhrases = p.EventPhrases
	}
	currentPrompts.Store(&d)
}

// CurrentPrompts returns the system prompts in use.
func CurrentPrompts() Prompts {
	if p := currentPrompts.Load(); p != nil {
		return *p
	}
	return DefaultPrompts()
}
