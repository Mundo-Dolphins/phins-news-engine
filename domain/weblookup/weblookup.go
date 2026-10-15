// Package weblookup models the bounded, application-controlled web
// evidence loop the offline catalog-verification flow uses:
// a sanitized search result shape, the strict action schema the local
// LLM must respond with (final vs. web_search — never an arbitrary tool
// call or a URL to fetch), deterministic query templates, and the query
// validation gate every LLM-proposed query string must pass before the
// application executes a real search. This package has no I/O.
package weblookup

import (
	"fmt"
	"strings"
)

// SearchResult is one sanitized web-search hit: title, URL, domain and a
// short snippet only — never a full page body.
// Trusted is set by the WebSearcher adapter itself; an untrusted result
// is expected to be dropped before it ever reaches this type's callers
// (see app/catalogverification), so in practice every
// SearchResult that survives to the LLM or a proposal has Trusted == true.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
	Domain  string
	Trusted bool
}

// Supported AnalysisAction.Action values. No other action is ever
// accepted — the application never executes an arbitrary tool call.
const (
	ActionFinal     = "final"
	ActionWebSearch = "web_search"
)

// Disposition is the LLM's verdict on a candidate in a final action.
type Disposition string

const (
	DispositionKeep      Disposition = "keep"
	DispositionReject    Disposition = "reject"
	DispositionUncertain Disposition = "uncertain"
)

// ParseDisposition normalizes s (case/whitespace-insensitive). An unknown
// value yields (DispositionUncertain, false): malformed LLM output is
// never treated as a keep or a reject.
func ParseDisposition(s string) (Disposition, bool) {
	switch d := Disposition(strings.ToLower(strings.TrimSpace(s))); d {
	case DispositionKeep, DispositionReject, DispositionUncertain:
		return d, true
	}
	return DispositionUncertain, false
}

// AnalysisAction is the local LLM's structured response during the
// bounded catalog-verification tool loop. The
// disposition and entity fields are only meaningful on a final action.
// EntityType is the raw LLM string; the application
// validates it.
type AnalysisAction struct {
	Action        string
	Query         string
	Disposition   Disposition
	CanonicalName string
	EntityType    string
	// Metadata holds only allow-listed keys: sport, league, position,
	// role, team, team_id, nfl_context.
	Metadata map[string]string
	Reason   string
}

// QueryKind selects one of the deterministic query templates from issue
// #225 Part 11, preferred over letting the LLM invent a query from
// scratch for every request.
type QueryKind int

const (
	QueryUnknownPlayer QueryKind = iota
	QueryPositionVerification
	QueryTeamRosterRelation
	QueryCoachOrExecutive
)

// TeamNames scopes the team-specific query templates: Full is the team's
// full name ("Springfield Isotopes") and Short its usual short name
// ("Isotopes"). Both come from the host's team data; when they are empty
// the templates fall back to the league ("NFL").
type TeamNames struct {
	Full  string
	Short string
}

// BuildTemplateQuery renders one of the fixed query templates for name,
// scoped to team. An unrecognized kind falls back to the unknown-player
// template, the most general of the four.
func BuildTemplateQuery(kind QueryKind, name string, team TeamNames) string {
	short := strings.TrimSpace(team.Short)
	full := strings.TrimSpace(team.Full)
	if short == "" {
		short = full
	}
	if short == "" {
		short = "NFL"
	}
	if full == "" {
		full = short
	}
	switch kind {
	case QueryPositionVerification:
		return fmt.Sprintf("%q NFL position", name)
	case QueryTeamRosterRelation:
		return fmt.Sprintf("%q %s", name, full)
	case QueryCoachOrExecutive:
		return fmt.Sprintf("%q %s coach", name, short)
	case QueryUnknownPlayer:
		fallthrough
	default:
		return fmt.Sprintf("%q %s", name, short)
	}
}

// maxQueryLength bounds an LLM-proposed query before it is ever executed
// as a real search.
const maxQueryLength = 200

// disallowedQueryChars rejects characters with no legitimate place in a
// search-engine query string, most importantly markup delimiters that
// would signal an attempt to smuggle something other than search terms
// through this gate.
const disallowedQueryChars = "<>{}"

// ValidateQuery trims and validates a query string proposed by the LLM
// before the application executes it as a real web search. It never
// accepts, validates, or resolves a URL — the LLM can only ever propose
// search terms here, never a fetch target.
func ValidateQuery(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return "", fmt.Errorf("weblookup: query must not be empty")
	}
	if len(q) > maxQueryLength {
		return "", fmt.Errorf("weblookup: query exceeds %d characters", maxQueryLength)
	}
	if strings.ContainsAny(q, disallowedQueryChars) {
		return "", fmt.Errorf("weblookup: query contains disallowed characters")
	}
	return q, nil
}

// IsTrustedDomain reports whether host exactly matches one of
// trustedDomains, or is a real subdomain of one of them. host and every
// entry of trustedDomains are normalized (lowercased, trailing dot
// stripped) before comparing. This is a plain suffix/equality check, not
// a substring match, so a lookalike domain such as
// "league.example.com.attacker.test" never matches a trusted "league.example.com".
// Both the WebSearcher adapter and catalogverification.Service's cache-hit
// path call this so the two never drift.
func IsTrustedDomain(host string, trustedDomains []string) bool {
	h := normalizeHostname(host)
	if h == "" {
		return false
	}
	for _, d := range trustedDomains {
		nd := normalizeHostname(d)
		if nd == "" {
			continue
		}
		if h == nd || strings.HasSuffix(h, "."+nd) {
			return true
		}
	}
	return false
}

func normalizeHostname(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}
