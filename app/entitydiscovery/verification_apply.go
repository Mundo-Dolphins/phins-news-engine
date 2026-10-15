package entitydiscovery

import (
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// overlayKeys are the metadata keys a verified result overrides.
var overlayKeys = []string{"sport", "league", "position", "role", "team", "team_id", MetadataAffiliationStatus}

// AmbiguousTokens maps a normalized bare team token (a city or a
// nickname shared by several teams) to the normalized verified names it
// legitimately belongs to; as an alias of anything else it is a
// cross-team leak and ApplyVerification scrubs it. A token mapped to no
// owner (typically the covered team's bare city name) is always scrubbed.
// Empty by
// default: the tokens are specific to the league and team a host program
// covers, so it sets them once at startup, e.g.
//
//	entitydiscovery.AmbiguousTokens = map[string][]string{
//		"springfield": {},
//		"tempests":    {"springfield tempests", "pinecrest tempests"},
//	}
var AmbiguousTokens = map[string][]string{}

// ApplyVerification folds a verifier outcome into a proposal. It is pure,
// idempotent and never mutates p's maps or slices. Uncertain returns p
// unchanged; Reject marks it rejected (auto_rejected_non_nfl) keeping
// all evidence; Keep applies the verified identity and metadata and
// scrubs aliases that point at other entities. A team Keep that is
// generic (single token, no verified team_id) or that names an existing
// catalog team other than the proposal's target is treated as uncertain.
func ApplyVerification(p entityproposal.Proposal, vr VerifyResult, catalog CatalogIndex, knownTeamIDs map[string]bool) entityproposal.Proposal {
	switch vr.Disposition {
	case weblookup.DispositionReject:
		out := cloneProposal(p)
		overlayMetadata(out.Metadata, vr.Metadata, []string{"sport", "league"})
		if vr.Reason != "" {
			out.Metadata["reason"] = vr.Reason
		}
		if len(vr.WebSources) > 0 {
			out.WebSources = append([]entityproposal.WebSource(nil), vr.WebSources...)
		}
		out.Status = entityproposal.StatusRejected
		out.DecisionReason = entityproposal.ReasonAutoRejectedNonNFL
		if vr.DecisionReason != "" {
			out.DecisionReason = vr.DecisionReason
		}
		out.Verification = vr.Provenance.toVerification()
		return out
	case weblookup.DispositionKeep:
	default:
		return p
	}

	out := cloneProposal(p)
	if vr.EntityType != "" {
		out.EntityType = vr.EntityType
	}
	if vr.CanonicalName != "" {
		if n := entityproposal.Normalize(vr.CanonicalName); n != out.NormalizedName && out.CanonicalName != "" {
			// Keep the observed surface form so the renamed proposal
			// still matches it (it goes through the alias scrub below).
			out.Aliases = mergeAliases(out.Aliases, []string{out.CanonicalName})
		}
		out.CanonicalName = vr.CanonicalName
		out.NormalizedName = entityproposal.Normalize(vr.CanonicalName)
	}
	overlayMetadata(out.Metadata, vr.Metadata, overlayKeys)
	resolveTeamID(out.Metadata, catalog, knownTeamIDs)
	normalizeAffiliation(out.Metadata, vr.Metadata, out.EntityType)
	if out.EntityType == entityproposal.EntityTypeTeam {
		teamID := out.Metadata["team_id"]
		if teamID == "" && len(strings.Fields(out.CanonicalName)) < 2 {
			// Generic single-token team name without a verified identity.
			return p
		}
		if teamID != "" && teamID != out.TargetEntityID {
			// Names an existing catalog team: not a new team.
			return p
		}
	}
	out.Confidence = vr.Confidence
	out.Verification = vr.Provenance.toVerification()
	if len(vr.WebSources) > 0 {
		// Structured (Wikidata) results carry no web sources: keep any
		// existing evidence instead of wiping it.
		out.WebSources = append([]entityproposal.WebSource(nil), vr.WebSources...)
	}

	verified := entityproposal.Normalize(out.CanonicalName)
	kept := out.Aliases[:0:0]
	for _, a := range out.Aliases {
		na := entityproposal.Normalize(a)
		if na == "" {
			continue
		}
		if ce, ok := catalog.Lookup(na); ok {
			if entityproposal.Normalize(ce.CanonicalName) != verified ||
				(out.TargetEntityID != "" && ce.ID != out.TargetEntityID) {
				continue
			}
		}
		if owners, ok := AmbiguousTokens[na]; ok && !contains(owners, verified) {
			continue
		}
		kept = append(kept, a)
	}
	out.Aliases = kept
	return out
}

// resolveTeamID keeps team_id only when it is a known catalog team. The
// LLM has to guess the id, so an unknown or missing id is resolved from
// the team name against team-type catalog entities; when that fails too
// the id is dropped but the team name is kept, so approval refuses the
// proposal instead of treating it as a person of the covered team.
func resolveTeamID(md map[string]string, catalog CatalogIndex, knownTeamIDs map[string]bool) {
	if id := md["team_id"]; id != "" && knownTeamIDs[id] {
		return
	}
	delete(md, "team_id")
	if id, ok := catalog.TeamIDByName(entityproposal.Normalize(md["team"])); ok && knownTeamIDs[id] {
		md["team_id"] = id
	}
}

// normalizeAffiliation keeps affiliation_status consistent with the
// team fields after an overlay. Free agency needs positive
// evidence from the verifier: a free_agent claim that also names a team
// is contradictory and becomes unknown; a clean free_agent claim for a
// player supersedes stale team data. A resolved team_id supersedes a
// stale free_agent/unknown status, and a team claim that could not be
// resolved to a known team_id is never left as "team".
func normalizeAffiliation(md, verified map[string]string, et entityproposal.EntityType) {
	key := MetadataAffiliationStatus
	status, err := eventsignals.ParseAffiliationStatus(md[key])
	if err != nil || status == eventsignals.AffiliationUnspecified {
		delete(md, key)
		status = eventsignals.AffiliationUnspecified
	}
	if status == eventsignals.AffiliationFreeAgent && et != entityproposal.EntityTypePlayer {
		delete(md, key)
		return
	}
	claimsTeam := verified["team"] != "" || verified["team_id"] != ""
	switch {
	case status == eventsignals.AffiliationFreeAgent && verified[key] == string(status):
		if claimsTeam {
			md[key] = string(eventsignals.AffiliationUnknown)
			delete(md, "team_id")
			return
		}
		delete(md, "team")
		delete(md, "team_id")
	case md["team_id"] != "":
		md[key] = string(eventsignals.AffiliationTeam)
	case status == eventsignals.AffiliationTeam:
		md[key] = string(eventsignals.AffiliationUnknown)
	case status == eventsignals.AffiliationFreeAgent && md["team"] != "":
		md[key] = string(eventsignals.AffiliationUnknown)
	}
}

func overlayMetadata(dst, src map[string]string, keys []string) {
	for _, k := range keys {
		if v := src[k]; v != "" {
			dst[k] = v
		}
	}
	for k, v := range dst {
		if v == "" {
			delete(dst, k)
		}
	}
}

// suggestionKeys are the metadata keys owned by the league gazetteer.
var suggestionKeys = []string{"suggested_entity", "suggested_team", "suggested_basis", "suggested_candidates"}

// ApplySuggestions returns p with its suggested_* metadata replaced by s (nil
// clears them). It never mutates p.
func ApplySuggestions(p entityproposal.Proposal, s map[string]string) entityproposal.Proposal {
	out := cloneProposal(p)
	for _, k := range suggestionKeys {
		delete(out.Metadata, k)
	}
	for _, k := range suggestionKeys {
		if v := s[k]; v != "" {
			out.Metadata[k] = v
		}
	}
	return out
}

func cloneProposal(p entityproposal.Proposal) entityproposal.Proposal {
	out := p
	out.Metadata = make(map[string]string, len(p.Metadata))
	for k, v := range p.Metadata {
		out.Metadata[k] = v
	}
	out.Aliases = append([]string(nil), p.Aliases...)
	out.Sources = append([]entityproposal.Source(nil), p.Sources...)
	out.WebSources = append([]entityproposal.WebSource(nil), p.WebSources...)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
