// Package entityidentity converts already-fetched structured knowledge
// (Wikidata candidates and entities, see domain/knowledge) into
// the project's entity semantics: exactly one of verified_nfl,
// verified_non_nfl, ambiguous or not_found, plus canonical identity and
// enrichment metadata when the data supports it.
//
// It is pure and deterministic: no network, no LLM, no clock (the caller
// passes Now), and the result never depends on map iteration or statement
// order. Missing or stale data yields partial metadata, never invention;
// when in doubt the package prefers a false negative.
package entityidentity

import (
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

// Status is the single verdict Resolve returns.
type Status string

const (
	StatusVerifiedNFL    Status = "verified_nfl"
	StatusVerifiedNonNFL Status = "verified_non_nfl"
	StatusAmbiguous      Status = "ambiguous"
	StatusNotFound       Status = "not_found"
)

// Reasons explaining a verdict (stable strings for audit/logging).
const (
	ReasonNoCandidates       = "no_candidates"
	ReasonNoNameMatch        = "no_name_match"
	ReasonPartialNameOnly    = "partial_name_only"
	ReasonLowScore           = "score_below_threshold"
	ReasonCloseMargin        = "margin_too_small"
	ReasonNeedsCorroboration = "needs_context_corroboration"
	ReasonEntityNotFetched   = "entity_not_fetched"
	ReasonUnclassified       = "unclassified_entity"
	ReasonUnknownLeague      = "league_unknown"
	ReasonNFLEvidence        = "nfl_membership"
	ReasonNonNFL             = "not_nfl"
)

// Scoring weights and thresholds. Name evidence dominates; sport/league
// signals are deliberately small so two same-name people in different
// sports stay ambiguous without context, and search rank is only a
// tie-breaker.
const (
	scoreLabelExact = 40
	scoreLabelNorm  = 35
	scoreAlias      = 25
	scorePartial    = 15

	scoreKindMatch    = 15
	scoreKindMismatch = -25
	scoreKindNeutral  = 10 // classifiable entity when no kind is expected
	scoreCatalog      = 20 // candidate is a known local catalog entity
	scoreContextID    = 25 // candidate is a catalog entity present in the article
	scoreContextTeam  = 20 // candidate's current team is present in the article
	scoreFootball     = 3
	scoreNFL          = 5
	maxRankBonus      = 2

	// MinScore is the lowest top score that may be verified.
	MinScore = 50
	// MinMargin is the smallest lead over the runner-up that may be verified.
	MinMargin = 15
	// MaxCandidates bounds how many candidates are scored.
	MaxCandidates = 10
	maxRanking    = 5
)

// CatalogEntry is a local catalog entity.
type CatalogEntry struct {
	ID            string
	CanonicalName string
	Kind          entityproposal.EntityType
}

// Catalog resolves a normalized (entityproposal.Normalize) name to the
// local entity owning it as canonical name or alias.
type Catalog interface {
	Lookup(normalizedName string) (CatalogEntry, bool)
}

// Input is everything Resolve needs; all of it is already fetched.
type Input struct {
	// Name is the observed candidate text.
	Name string
	// ExpectedKind is the extractor's entity type; empty when unknown.
	ExpectedKind entityproposal.EntityType
	// ContextEntityIDs are local catalog IDs of entities found in the
	// same article.
	ContextEntityIDs []string
	Catalog          Catalog
	// Candidates are the provider's search hits, in provider order (used
	// only as a weak tie-breaker).
	Candidates []knowledge.Candidate
	// Entities holds the candidates' entities plus the team entities they
	// reference (see ReferencedIDs), keyed by requested ID.
	Entities map[string]knowledge.Entity
	// Now is the instant "current" refers to.
	Now time.Time
}

// RankedCandidate records one scored candidate for audit.
type RankedCandidate struct {
	ID    string
	Label string
	Score int
}

// AliasRejection records an alias that was not proposed and why.
type AliasRejection struct {
	Alias  string
	Reason string
}

// Result is Resolve's verdict. Metadata uses the keys of #302
// (provider, provider_id, sport, league, position, team, team_id, role)
// and only holds values the data supports.
type Result struct {
	Status        Status
	Reason        string
	EntityType    entityproposal.EntityType
	CanonicalName string
	Metadata      map[string]string
	// Aliases are safe to add for the identified local entity: the observed
	// text only, never Wikidata aliases.
	Aliases         []string
	RejectedAliases []AliasRejection
	Ranking         []RankedCandidate
	// AffiliationReason explains an unknown player affiliation with one of
	// the ReasonAff* strings; empty when the affiliation is a team (or not
	// applicable). It is diagnostic only: affiliation_status stays in
	// Metadata.
	AffiliationReason string
}

type scored struct {
	id      string
	label   string
	score   int
	tier    int
	ctx     bool // corroborated by article context
	entity  knowledge.Entity
	fetched bool
	prof    profile
}

// Resolve decides what in.Name refers to. See the package comment.
func Resolve(in Input) Result {
	observed := entityproposal.Normalize(in.Name)
	if observed == "" || len(in.Candidates) == 0 {
		return Result{Status: StatusNotFound, Reason: ReasonNoCandidates}
	}
	ctxIDs := map[string]bool{}
	for _, id := range in.ContextEntityIDs {
		ctxIDs[id] = true
	}

	var all []scored
	seen := map[string]bool{}
	for i, c := range in.Candidates {
		if len(all) >= MaxCandidates {
			break
		}
		e, fetched := in.Entities[c.ID]
		id := c.ID
		if fetched && e.CanonicalID != "" {
			id = e.CanonicalID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		all = append(all, scoreCandidate(in, observed, ctxIDs, c, e, fetched, i))
	}

	eligible := all[:0:0]
	for _, s := range all {
		if s.tier > 0 {
			eligible = append(eligible, s)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].score != eligible[j].score {
			return eligible[i].score > eligible[j].score
		}
		return qidLess(eligible[i].id, eligible[j].id)
	})
	ranking := make([]RankedCandidate, 0, maxRanking)
	for i, s := range eligible {
		if i == maxRanking {
			break
		}
		ranking = append(ranking, RankedCandidate{ID: s.id, Label: s.label, Score: s.score})
	}
	if len(eligible) == 0 {
		return Result{Status: StatusNotFound, Reason: ReasonNoNameMatch}
	}

	top := eligible[0]
	if top.tier < scoreAlias && !top.ctx && !anyViable(eligible) {
		return Result{Status: StatusNotFound, Reason: ReasonPartialNameOnly, Ranking: ranking}
	}

	res := Result{Status: StatusAmbiguous, Ranking: ranking, EntityType: "", Metadata: map[string]string{}}
	margin := top.score
	if len(eligible) > 1 {
		margin = top.score - eligible[1].score
	}
	if !top.fetched {
		res.Reason = ReasonEntityNotFetched
		return res
	}
	res.CanonicalName = top.label
	res.EntityType = top.prof.entityType(in.ExpectedKind)
	res.Metadata["provider"] = providerWikidata
	res.Metadata["provider_id"] = top.id

	needsCorroboration := top.tier < scoreLabelNorm || len(strings.Fields(observed)) == 1
	switch {
	case top.score < MinScore:
		res.Reason = ReasonLowScore
	case margin < MinMargin:
		res.Reason = ReasonCloseMargin
	case needsCorroboration && !top.ctx:
		res.Reason = ReasonNeedsCorroboration
	case !top.prof.isPerson && !top.prof.isTeam:
		res.EntityType = ""
		res.Reason = ReasonUnclassified
	default:
		verdict(&res, in, top)
		if res.Status == StatusVerifiedNFL {
			res.Aliases, res.RejectedAliases = safeAliases(in, top, res)
		}
	}
	return res
}

func anyViable(es []scored) bool {
	for _, s := range es {
		if s.tier >= scoreAlias || s.ctx {
			return true
		}
	}
	return false
}

// qidLess orders Wikidata IDs numerically so ties break deterministically.
func qidLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func scoreCandidate(in Input, observed string, ctxIDs map[string]bool, c knowledge.Candidate, e knowledge.Entity, fetched bool, rank int) scored {
	s := scored{id: c.ID, label: c.Label, entity: e, fetched: fetched}
	aliases := c.Aliases
	if fetched {
		s.id = c.ID
		if e.CanonicalID != "" {
			s.id = e.CanonicalID
		}
		if e.Label != "" {
			s.label = e.Label
		}
		aliases = append(append([]string{}, aliases...), e.Aliases...)
		s.prof = classify(e)
	}

	s.tier = nameTier(in.Name, observed, s.label, aliases)
	if s.tier == 0 {
		return s
	}
	s.score = s.tier

	// Kind.
	typ := s.prof.entityType(in.ExpectedKind)
	switch {
	case in.ExpectedKind == "":
		if typ != "" {
			s.score += scoreKindNeutral
		}
	case typ == in.ExpectedKind:
		s.score += scoreKindMatch
	default:
		s.score += scoreKindMismatch
	}

	// Local catalog and article context.
	if in.Catalog != nil {
		if ce, ok := in.Catalog.Lookup(entityproposal.Normalize(s.label)); ok {
			s.score += scoreCatalog
			if ctxIDs[ce.ID] {
				s.score += scoreContextID
				s.ctx = true
			}
		}
	}
	if fetched && s.prof.isPerson && in.Catalog != nil {
		if tid := currentMembership(e, in.Now); tid != "" {
			if t, ok := in.Entities[tid]; ok {
				if ce, ok := in.Catalog.Lookup(entityproposal.Normalize(t.Label)); ok && ce.Kind == entityproposal.EntityTypeTeam && ctxIDs[ce.ID] {
					s.score += scoreContextTeam
					s.ctx = true
				}
			}
		}
	}

	// Sport / league.
	if s.prof.football {
		s.score += scoreFootball
	}
	if nflEvidence(s.id, s.prof, e, in.Entities) {
		s.score += scoreNFL
	}

	// Weak tie-breaker: provider search rank.
	if b := maxRankBonus - rank; b > 0 {
		s.score += b
	}
	return s
}

// nameTier grades how well the observed text names a candidate.
func nameTier(raw, observed, label string, aliases []string) int {
	if strings.EqualFold(strings.TrimSpace(raw), strings.TrimSpace(label)) {
		return scoreLabelExact
	}
	if entityproposal.Normalize(label) == observed {
		return scoreLabelNorm
	}
	for _, a := range aliases {
		if entityproposal.Normalize(a) == observed {
			return scoreAlias
		}
	}
	if containsTokens(entityproposal.Normalize(label), observed) {
		return scorePartial
	}
	return 0
}

// containsTokens reports whether needle's tokens appear contiguously in
// (and make up strictly less than) hay.
func containsTokens(hay, needle string) bool {
	if needle == "" || needle == hay {
		return false
	}
	return strings.Contains(" "+hay+" ", " "+needle+" ")
}

// nflEvidence reports whether the entity is, or has been, part of the NFL
// according to structured data.
func nflEvidence(id string, p profile, e knowledge.Entity, ents map[string]knowledge.Entity) bool {
	if p.isTeam {
		return p.nflTeam
	}
	if !p.isPerson {
		return false
	}
	if p.nflOcc {
		return true
	}
	for _, tid := range valuesOf(e, propMemberOf) {
		if t, ok := ents[tid]; ok && classify(t).nflTeam {
			return true
		}
	}
	return namesAsHeadCoach(id, ents)
}

// verdict fills res with the NFL / non-NFL decision for the chosen top
// candidate, whose identity is already certain.
func verdict(res *Result, in Input, top scored) {
	p := top.prof
	if p.isTeam {
		teamVerdict(res, p)
		return
	}
	personVerdict(res, in, top)
}

func teamVerdict(res *Result, p profile) {
	switch {
	case p.nflTeam:
		setNFL(res, ReasonNFLEvidence)
	case p.teamSport() != "":
		setNonNFL(res, p.teamSport(), firstLeague(p))
	default:
		res.Reason = ReasonUnknownLeague
		if p.football {
			res.Metadata["sport"] = sportAmericanFootball
		}
	}
}

func personVerdict(res *Result, in Input, top scored) {
	p := top.prof
	e := top.entity
	if nflEvidence(top.id, p, e, in.Entities) {
		setNFL(res, ReasonNFLEvidence)
		enrichPerson(res, in, top)
		return
	}
	if !p.football {
		sport := ""
		if len(p.sports) > 0 {
			sport = p.sports[0]
		}
		setNonNFL(res, sport, "")
		return
	}
	// Football-related but no NFL evidence: non-NFL only when every
	// membership is known (and none is NFL); otherwise we just don't know.
	teams := valuesOf(e, propMemberOf)
	if len(teams) == 0 {
		res.Metadata["sport"] = sportAmericanFootball
		res.Reason = ReasonUnknownLeague
		return
	}
	for _, tid := range teams {
		if _, ok := in.Entities[tid]; !ok {
			res.Metadata["sport"] = sportAmericanFootball
			res.Reason = ReasonUnknownLeague
			return
		}
	}
	setNonNFL(res, sportAmericanFootball, "")
}

// enrichPerson adds position/team/role only where structured data
// supports them; coach and executive affiliation is never derived from
// player memberships.
func enrichPerson(res *Result, in Input, top scored) {
	switch res.EntityType {
	case entityproposal.EntityTypePlayer:
		if pos := position(top.entity); pos != "" {
			res.Metadata["position"] = pos
		}
		enrichPlayerAffiliation(res, in, top.entity)
	case entityproposal.EntityTypeCoach:
		if tid := currentHeadCoachTeam(top.id, in.Entities, in.Now); tid != "" {
			if setTeam(res, in, tid, true) {
				res.Metadata["role"] = roleHeadCoach
			}
		}
	}
}

// enrichPlayerAffiliation sets affiliation_status. Only one safely current
// NFL team that maps to a local team entity yields "team"; anything else is
// "unknown" with a bounded reason. Missing Wikidata data is never evidence
// of free agency, so free_agent is never produced here.
func enrichPlayerAffiliation(res *Result, in Input, e knowledge.Entity) {
	tid, reason := membership(e, in.Now)
	if tid != "" {
		if setTeam(res, in, tid, true) && res.Metadata["team_id"] != "" {
			res.Metadata["affiliation_status"] = affiliationTeam
			return
		}
		delete(res.Metadata, "team")
		delete(res.Metadata, "team_id")
		reason = ReasonAffUnmapped
	}
	res.Metadata["affiliation_status"] = affiliationUnknown
	res.AffiliationReason = reason
}

// setTeam records the team when its entity is known and in the NFL.
func setTeam(res *Result, in Input, tid string, requireNFL bool) bool {
	t, ok := in.Entities[tid]
	if !ok || t.Label == "" {
		return false
	}
	if requireNFL && !classify(t).nflTeam {
		return false
	}
	res.Metadata["team"] = t.Label
	if in.Catalog != nil {
		if ce, ok := in.Catalog.Lookup(entityproposal.Normalize(t.Label)); ok && ce.Kind == entityproposal.EntityTypeTeam {
			res.Metadata["team_id"] = ce.ID
		}
	}
	return true
}

func setNFL(res *Result, reason string) {
	res.Status = StatusVerifiedNFL
	res.Reason = reason
	res.Metadata["sport"] = sportAmericanFootball
	res.Metadata["league"] = leagueNFL
}

func setNonNFL(res *Result, sport, league string) {
	res.Status = StatusVerifiedNonNFL
	res.Reason = ReasonNonNFL
	if sport != "" {
		res.Metadata["sport"] = sport
	}
	if league != "" {
		res.Metadata["league"] = league
	}
}

// safeAliases decides whether the observed text may become an alias of the
// identified entity. Only the observed text is ever proposed. Single-token
// aliases are generic ("Springfield"), and aliases owned by another local entity
// would pollute it, so both are rejected.
func safeAliases(in Input, top scored, res Result) ([]string, []AliasRejection) {
	observed := entityproposal.Normalize(in.Name)
	label := strings.TrimSpace(res.CanonicalName)
	if observed == "" || observed == entityproposal.Normalize(label) {
		return nil, nil
	}
	text := strings.TrimSpace(in.Name)
	if len(strings.Fields(observed)) < 2 {
		return nil, []AliasRejection{{Alias: text, Reason: "generic_single_token"}}
	}
	if in.Catalog != nil {
		owner, owned := in.Catalog.Lookup(observed)
		self, hasSelf := in.Catalog.Lookup(entityproposal.Normalize(label))
		if owned && (!hasSelf || owner.ID != self.ID) {
			return nil, []AliasRejection{{Alias: text, Reason: "collides_with_" + owner.ID}}
		}
	}
	return []string{text}, nil
}
