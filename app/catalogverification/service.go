// app/catalogverification/service.go
//
// Package catalogverification implements the bounded, per-candidate
// web-lookup tool loop for the offline catalog maintenance process:
// given one uncertain proposal candidate, it lets the
// local LLM either finalize immediately or request a validated,
// budget-bounded web search, executed by an application-controlled
// ports/out.WebSearcher — never by the LLM itself. It is imported only
// by the tools CLI (via app/entitydiscovery's
// Verifier interface) — never by the service binary (ingestion) or
// app/entityresolution (the online fallback). Every
// failure mode (LLM error, search provider error, cache error, invalid/
// hallucinated action, exhausted search budget) degrades the candidate
// to an Uncertain VerifyResult; Verify never returns an error, by
// design, matching entityresolution.Resolve's fail-open contract.
package catalogverification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// WebSearchCache persists web-search results keyed by a hash of
// (provider, normalized query) so the same query is never re-sent to
// the provider within its TTL.
type WebSearchCache interface {
	Get(ctx context.Context, queryHash string) ([]weblookup.SearchResult, bool, error)
	Set(ctx context.Context, queryHash, provider, query string, results []weblookup.SearchResult, ttl time.Duration) error
}

// VerifyRequest is one candidate's current state going into the tool
// loop.
type VerifyRequest struct {
	Name       string
	Type       string
	Metadata   map[string]string
	Confidence float64
	Sources    []entityproposal.Source
	// Articles, when set, replaces the title-only local evidence derived from
	// Sources with fuller text (title + summary).
	Articles        []appout.EntityDiscoveryArticle
	FallbackHistory []appout.FallbackSignal
}

// VerifyResult is the tool loop's outcome for one candidate. Disposition
// is Uncertain whenever the loop could not reach a trustworthy "final"
// action backed by trusted evidence; Resolved is true iff
// Disposition != Uncertain. Callers must not raise confidence or attach
// WebSources from an unresolved result.
type VerifyResult struct {
	Disposition   weblookup.Disposition
	CanonicalName string
	EntityType    string
	Metadata      map[string]string // req.Metadata overlaid by validated LLM metadata
	Confidence    float64
	WebSources    []entityproposal.WebSource
	Reason        string
	Resolved      bool // true iff Disposition != Uncertain
	// ProviderError marks an unresolved result caused by an outage of the
	// search provider (not by missing evidence): callers must not record the
	// candidate as attempted, so a later run retries it.
	ProviderError bool
}

// uncertain is the degraded outcome for every failure path.
func uncertain(reason string) VerifyResult {
	return VerifyResult{Disposition: weblookup.DispositionUncertain, Reason: reason}
}

// maxSteps bounds the tool loop independently of MaxSearchesPerCandidate
// — a "final" response is one step and doesn't consume search budget,
// so this guards against a pathological LLM that never terminates even
// while staying within its search budget.
const maxSteps = 3

// queryState records the outcome of one query within a single Verify call.
type queryState struct {
	trusted bool // produced at least one trusted result
	results int
}

// normalizeQuery makes case/whitespace variants of a query compare equal.
func normalizeQuery(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}

// Service runs the bounded per-candidate web-lookup tool loop.
//
// Service is not safe for concurrent use: searchesThisRun is mutated by
// Verify without synchronization. Callers must serialize calls to Verify
// on a single Service instance (or construct one Service per goroutine).
type Service struct {
	LLM      appout.EntityDiscoveryLLM
	Searcher appout.WebSearcher
	Cache    WebSearchCache
	Logger   appout.Logger
	Config   config.WebLookupConfig
	// searchesThisRun counts every search executed across every Verify
	// call sharing this Service instance, enforcing
	// Config.MaxSearchesPerRun. Zero value is a valid starting state —
	// callers construct one Service per `entities discover --verify`
	// invocation. Unsynchronized: see the Service doc comment.
	searchesThisRun int

	// Now and Wait are test seams for search pacing; nil means the real
	// clock and a context-aware sleep.
	Now  func() time.Time
	Wait func(ctx context.Context, d time.Duration)
	// Jitter returns a random extra delay in [0, max); nil means crypto/rand.
	Jitter func(max time.Duration) time.Duration

	lastSearchAt        time.Time
	consecutiveFailures int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// pace blocks until Config.MinSearchIntervalMS has elapsed since the
// previous provider call, keeping long runs under upstream rate limits.
func (s *Service) pace(ctx context.Context) {
	interval := time.Duration(s.Config.MinSearchIntervalMS) * time.Millisecond
	if interval <= 0 || s.lastSearchAt.IsZero() {
		return
	}
	remaining := interval - s.now().Sub(s.lastSearchAt)
	if remaining <= 0 {
		return
	}
	remaining += s.jitter(interval * 3 / 10)
	if s.Wait != nil {
		s.Wait(ctx, remaining)
		return
	}
	t := time.NewTimer(remaining)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// jitter spreads paced calls so a long run does not hit the provider on a
// perfectly regular beat.
func (s *Service) jitter(max time.Duration) time.Duration {
	if s.Jitter != nil {
		return s.Jitter(max)
	}
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// Pacing still enforces the minimum interval if entropy is unavailable.
		return 0
	}
	return time.Duration(n.Int64())
}

// circuitOpen reports whether enough consecutive searches have failed that
// the provider should be left alone for the rest of this run.
func (s *Service) circuitOpen() bool {
	limit := s.Config.MaxConsecutiveProviderFailures
	return limit > 0 && s.consecutiveFailures >= limit
}

// Searches returns the total provider searches executed this run.
func (s *Service) Searches() int { return s.searchesThisRun }

// Verify runs the bounded tool loop for one candidate. It never returns
// an error: any failure degrades the candidate to an Uncertain result.
func (s *Service) Verify(ctx context.Context, req VerifyRequest) VerifyResult {
	s.Logger.Info("catalog verification: candidate requested verification", "candidate", req.Name)

	var accumulated []weblookup.SearchResult
	searchesThisCandidate := 0
	localEvidence := toLocalEvidence(req.Sources)
	if len(req.Articles) > 0 {
		localEvidence = req.Articles
	}
	// queries tracks, per candidate, every normalized query already
	// resolved (by provider or cache) and whether it produced trusted
	// results.
	queries := map[string]queryState{}
	// finalOnly is set once the model must answer: it repeated a query, or the
	// candidate's searches are spent. Without it the next prompt would be
	// byte-identical and the deterministic model would ask for the same search
	// again until maxSteps ran out ("tool loop exhausted").
	finalOnly := false

	for step := 0; step < maxSteps; step++ {
		// A search requested on the last step could never be answered, so that
		// step is always final-only. Cache hits do not spend the candidate's
		// provider-search budget, so only provider calls count here.
		if searchesThisCandidate >= s.Config.MaxSearchesPerCandidate || step == maxSteps-1 {
			finalOnly = true
		}
		action, err := s.LLM.VerifyCandidate(ctx, appout.VerifyCandidateRequest{
			CandidateName: req.Name, CandidateType: req.Type, CandidateMetadata: req.Metadata,
			LocalEvidence: localEvidence, SearchResults: accumulated, FallbackHistory: req.FallbackHistory,
			FinalOnly: finalOnly,
		})
		if err != nil {
			s.Logger.Warn("catalog verification: LLM call failed", "candidate", req.Name, "error", err)
			return uncertain("LLM call failed")
		}

		switch action.Action {
		case weblookup.ActionFinal:
			result := NormalizeFinal(req, action, toWebSources(accumulated))
			s.Logger.Info("catalog verification: final action normalized", "candidate", req.Name, "disposition", string(result.Disposition), "web_sources", len(result.WebSources))
			return result

		case weblookup.ActionWebSearch:
			if finalOnly {
				s.Logger.Warn("catalog verification: model ignored the final-only instruction", "candidate", req.Name)
				return uncertain("model ignored the final-only instruction")
			}
			query, err := weblookup.ValidateQuery(action.Query)
			if err != nil {
				s.Logger.Warn("catalog verification: rejected invalid query", "candidate", req.Name, "error", err)
				return uncertain("invalid search query")
			}

			qkey := normalizeQuery(query)
			if state, seen := queries[qkey]; seen {
				if !state.trusted {
					// The LLM is re-asking a query already known to yield no
					// trusted results; looping again can never add evidence.
					s.Logger.Warn("catalog verification: repeated search has no new evidence", "candidate", req.Name, "query", query)
					return uncertain("repeated search has no new evidence")
				}
				// Known-useful query: its evidence is already accumulated,
				// so a repeat cannot duplicate WebSources.
				s.Logger.Info("catalog verification: repeated search reused earlier evidence", "candidate", req.Name, "results", state.results)
				finalOnly = true
				continue
			}

			if cached, ok := s.cached(ctx, query); ok {
				queries[qkey] = queryState{trusted: len(cached) > 0, results: len(cached)}
				s.Logger.Info("catalog verification: cache hit", "provider", s.Config.Provider, "candidate", req.Name, "results", len(cached))
				accumulated = appendUnique(accumulated, cached)
				continue
			}
			if s.circuitOpen() {
				res := uncertain("search provider circuit open")
				res.ProviderError = true
				return res
			}
			if searchesThisCandidate >= s.Config.MaxSearchesPerCandidate || s.searchesThisRun >= s.Config.MaxSearchesPerRun {
				s.Logger.Warn("catalog verification: search budget exhausted", "candidate", req.Name)
				return uncertain("search budget exhausted")
			}

			// Budget counters measure provider calls, not LLM tool actions.
			// They are incremented before the call so a failed/throttled
			// provider request still consumes run budget.
			searchesThisCandidate++
			s.searchesThisRun++
			s.pace(ctx)
			results, err := s.searchUncached(ctx, query)
			s.lastSearchAt = s.now()
			if err != nil {
				s.consecutiveFailures++
				s.Logger.Warn("catalog verification: search failed", "candidate", req.Name, "error", err)
				if s.circuitOpen() && s.consecutiveFailures == s.Config.MaxConsecutiveProviderFailures {
					s.Logger.Warn("catalog verification: search provider circuit open, no more searches this run", "consecutive_failures", s.consecutiveFailures)
				}
				res := uncertain("search failed")
				res.ProviderError = true
				return res
			}
			s.consecutiveFailures = 0
			queries[qkey] = queryState{trusted: len(results) > 0, results: len(results)}
			s.Logger.Info("catalog verification: search executed", "candidate", req.Name, "results", len(results))
			accumulated = appendUnique(accumulated, results)

		default:
			s.Logger.Warn("catalog verification: hallucinated action rejected", "candidate", req.Name, "action", action.Action)
			return uncertain("unknown action")
		}
	}

	s.Logger.Warn("catalog verification: tool loop exhausted without a final action", "candidate", req.Name)
	return uncertain("no final action")
}

// allowedEntityTypes are the entity types a final action may assign.
var allowedEntityTypes = map[string]bool{"player": true, "coach": true, "executive": true, "team": true}

// isAmericanFootball reports whether a free-text sport label from the LLM or
// a provider means American football. The label arrives in many spellings
// ("football", "American Football", "american_football", "tackle football"),
// so it is normalized rather than compared to one literal; Canadian and
// association football are different sports.
func isAmericanFootball(sport string) bool {
	s := strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(sport))
	if strings.TrimSpace(s) == "nfl" {
		return true
	}
	if !strings.Contains(s, "football") {
		return false
	}
	return !strings.Contains(s, "canadian") && !strings.Contains(s, "association")
}

// NormalizeFinal turns the LLM's final action into a VerifyResult. It is
// pure and is the single place disposition is validated:
// keep/reject need at least one trusted web source (no evidence =>
// uncertain, never reject); a non-NFL league forces reject (except an
// NCAA draft prospect); a reject whose own metadata says NFL/football is
// self-contradicting and becomes uncertain; keep requires a football sport and, for teams, a
// specific team identity. Confidence comes from AdjustConfidence, never
// from the LLM.
func NormalizeFinal(req VerifyRequest, a weblookup.AnalysisAction, sources []entityproposal.WebSource) VerifyResult {
	res := VerifyResult{
		Disposition:   weblookup.DispositionUncertain,
		CanonicalName: strings.TrimSpace(a.CanonicalName),
		EntityType:    req.Type,
		Confidence:    AdjustConfidence(req.Confidence, sources),
		WebSources:    sources,
		Reason:        strings.TrimSpace(a.Reason),
	}
	if res.CanonicalName == "" {
		res.CanonicalName = req.Name
	}
	if t := strings.ToLower(strings.TrimSpace(a.EntityType)); allowedEntityTypes[t] {
		res.EntityType = t
	}

	// Overlay validated LLM metadata; empty values are treated as absent.
	md := make(map[string]string, len(req.Metadata)+len(a.Metadata))
	for k, v := range req.Metadata {
		md[k] = v
	}
	for k, v := range a.Metadata {
		if v = strings.TrimSpace(v); v != "" {
			md[k] = v
		}
	}
	res.Metadata = md

	disp, _ := weblookup.ParseDisposition(string(a.Disposition))
	if disp == weblookup.DispositionUncertain {
		return res
	}
	if len(sources) == 0 {
		res.Reason = "no trusted evidence"
		return res
	}

	league := strings.ToUpper(md["league"])
	isFootball := isAmericanFootball(md["sport"])
	sport := strings.ToLower(md["sport"])
	switch {
	case league != "" && league != "NFL":
		if disp == weblookup.DispositionKeep && league == "NCAA" && md["nfl_context"] == "draft_prospect" {
			break
		}
		disp = weblookup.DispositionReject
	// An explicit NFL league is authoritative: a stray sport label such as
	// "NFL" or "gridiron" must not turn a verified NFL keep into a reject.
	case disp == weblookup.DispositionKeep && sport != "" && !isFootball && league != "NFL":
		disp = weblookup.DispositionReject
	case disp == weblookup.DispositionReject && (league == "NFL" || isFootball):
		// A reject that itself says NFL/football contradicts the policy
		// (only non-NFL entities are rejected); never auto-hide on it.
		res.Reason = "reject contradicts NFL/football metadata"
		return res
	}

	if disp == weblookup.DispositionKeep && res.EntityType == "team" &&
		md["team_id"] == "" && len(strings.Fields(strings.TrimSpace(a.CanonicalName))) < 2 {
		res.Reason = "generic team name without a specific team identity"
		return res
	}

	res.Disposition = disp
	res.Resolved = true
	return res
}

// cached returns trusted cached results for query. Cache failures are treated
// as misses so verification remains fail-open, matching the old search path.
func (s *Service) cached(ctx context.Context, query string) ([]weblookup.SearchResult, bool) {
	key := cacheKey(s.Config.Provider, query)
	if cached, ok, err := s.Cache.Get(ctx, key); err == nil && ok {
		// Re-apply the trust filter even on a cache hit: a value cached
		// under a wider Config.TrustedDomains must not flow to the LLM
		// or WebSources just because it predates a narrower config.
		return filterTrusted(cached, s.Config.TrustedDomains), true
	}
	return nil, false
}

// searchUncached calls s.Searcher, stores fresh results under the cache TTL,
// and filters to trusted domains only — an untrusted result is dropped here,
// before it ever reaches the LLM or a proposal.
func (s *Service) searchUncached(ctx context.Context, query string) ([]weblookup.SearchResult, error) {
	key := cacheKey(s.Config.Provider, query)

	results, err := s.Searcher.Search(ctx, query, appout.SearchOptions{
		MaxResults:     s.Config.MaxResultsPerSearch,
		TrustedDomains: s.Config.TrustedDomains,
	})
	if err != nil {
		return nil, err
	}

	trusted := filterTrusted(results, s.Config.TrustedDomains)
	if len(trusted) > 0 {
		s.Logger.Info("catalog verification: trusted results found", "count", len(trusted))
	}

	if err := s.Cache.Set(ctx, key, s.Config.Provider, query, trusted, s.Config.CacheTTL); err != nil {
		s.Logger.Warn("catalog verification: cache write failed", "error", err)
	}
	return trusted, nil
}

// filterTrusted keeps only results that are both marked Trusted and
// still belong to trustedDomains. Applied to both a fresh provider
// response and a cache hit, so a result cached under a wider or
// since-changed TrustedDomains list can never reach the LLM or a
// proposal just because it predates the current config.
func filterTrusted(results []weblookup.SearchResult, trustedDomains []string) []weblookup.SearchResult {
	trusted := make([]weblookup.SearchResult, 0, len(results))
	for _, r := range results {
		if r.Trusted && weblookup.IsTrustedDomain(r.Domain, trustedDomains) {
			trusted = append(trusted, r)
		}
	}
	return trusted
}

// appendUnique appends results to accumulated, skipping any whose URL is
// already present — a repeated (e.g. cache-hit) query must not produce
// duplicate WebSource entries or duplicate evidence in the LLM prompt.
func appendUnique(accumulated, results []weblookup.SearchResult) []weblookup.SearchResult {
	seen := make(map[string]bool, len(accumulated))
	for _, r := range accumulated {
		seen[r.URL] = true
	}
	for _, r := range results {
		if seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		accumulated = append(accumulated, r)
	}
	return accumulated
}

func cacheKey(provider, query string) string {
	sum := sha256.Sum256([]byte(provider + query))
	return hex.EncodeToString(sum[:])
}

// toLocalEvidence maps a candidate's originating Sources into the
// EntityDiscoveryArticle shape the LLM prompt's LOCAL_EVIDENCE section
// expects, so the LLM can weigh local evidence before requesting a web
// search. entityproposal.Source
// carries no article summary, so Summary is left as the zero value.
func toLocalEvidence(sources []entityproposal.Source) []appout.EntityDiscoveryArticle {
	out := make([]appout.EntityDiscoveryArticle, 0, len(sources))
	for _, src := range sources {
		out = append(out, appout.EntityDiscoveryArticle{ArticleID: src.ArticleID, Title: src.ArticleTitle})
	}
	return out
}

func toWebSources(results []weblookup.SearchResult) []entityproposal.WebSource {
	out := make([]entityproposal.WebSource, 0, len(results))
	for _, r := range results {
		out = append(out, entityproposal.WebSource{Domain: r.Domain, URL: r.URL, Title: r.Title})
	}
	return out
}

// AdjustConfidence nudges base confidence using web evidence quality:
// local evidence alone is unchanged; one trusted
// source raises it a little; multiple sources from independent domains
// raise it further, capped just under 1.0. Confidence is never taken
// verbatim from the LLM — this is the only place it is allowed to move.
func AdjustConfidence(base float64, webSources []entityproposal.WebSource) float64 {
	domains := map[string]bool{}
	for _, ws := range webSources {
		domains[ws.Domain] = true
	}
	switch len(domains) {
	case 0:
		return base
	case 1:
		return capConfidence(base + 0.05)
	default:
		return capConfidence(base + 0.05 + 0.03*float64(len(domains)-1))
	}
}

func capConfidence(c float64) float64 {
	if c > 0.99 {
		return 0.99
	}
	return c
}
