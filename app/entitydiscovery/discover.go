// app/entitydiscovery/discover.go
package entitydiscovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ArticleLister is the read access Discover needs over stored articles —
// satisfied directly by the existing concrete
// *sqlite.ArticleRepository (not part of ports/out.ArticleRepository; see
// ListSince's own doc comment for why these are extra methods on the
// concrete type rather than a port).
type ArticleLister interface {
	ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error)
	// ListUnprocessedByEntityDiscovery applies limit after excluding
	// articles already processed for extractorVersion.
	ListUnprocessedByEntityDiscovery(ctx context.Context, since time.Time, extractorVersion string, limit int) ([]article.Article, error)
}

// DiscoverOptions configures one `entities discover` run.
type DiscoverOptions struct {
	// Since is the lower bound on article fetched_at; the zero value
	// means no lower bound.
	Since time.Time
	// BatchSize is how many articles are sent to the LLM per request.
	BatchSize int
	// Limit bounds how many articles are considered in total for this
	// run; unless Force is set, only unprocessed articles count.
	Limit int
	// ExtractorVersion tags every proposal/processed-article record
	// this run produces.
	ExtractorVersion string
	// Force, when true, reprocesses articles even if they're already
	// recorded as processed for ExtractorVersion.
	Force bool
	// VerifyMinConfidence is the confidence threshold ShouldVerify
	// checks a candidate against; ignored when verifier is nil.
	VerifyMinConfidence float64
	// League, when set, makes discovery skip noise mentions and mentions that
	// already resolve to a league player or an NFL team alias. Nil = off.
	League *leaguegazetteer.Index
}

// DiscoverResult summarizes one run for the CLI to print.
type DiscoverResult struct {
	ArticlesConsidered int
	BatchesTotal       int
	BatchesFailed      int
	ProposalsUpserted  int
	// AutoRejected counts proposals persisted as rejected because the
	// verifier found them not to be NFL entities.
	AutoRejected int
	// Uncertain counts verified proposals left pending unchanged, either
	// because the verifier was inconclusive or because persisting its
	// outcome conflicted with another proposal.
	Uncertain int
	// Duplicates counts proposals rejected as duplicate_of_existing
	// because verification renamed them onto an already-pending identity.
	Duplicates int
	// SkippedNoise counts extracted mentions dropped by the deterministic
	// noise filter; AlreadyKnownLeague counts those that already resolve to a
	// league player or NFL team (no proposal is opened for them).
	SkippedNoise       int
	AlreadyKnownLeague int
	BatchErrors        []string
	// Verification reports the verification workload of this run.
	Verification VerificationMetrics
}

// VerificationMetrics describes how much verification work a run created.
type VerificationMetrics struct {
	CandidatesConsidered int        // aggregates after catalog filtering (new proposals considered)
	CatalogHits          int        // extracted entities that exactly matched the local catalog
	Chain                ChainStats // zero unless the verifier implements StatsReporter
	LeftForReview        int        // == DiscoverResult.Uncertain; duplicated here for printing
}

// aggregate accumulates one proposal's data across every batch of a
// single Discover run, before a single repo.Upsert call per aggregate
// at the end. Cross-run aggregation (merging into a still-pending
// proposal from an earlier invocation) is the repository's job — see
// EntityProposalRepository.Upsert.
type aggregate struct {
	canonicalName  string
	normalizedName string
	entityType     entityproposal.EntityType
	kind           entityproposal.Kind
	targetEntityID string
	metadata       map[string]string
	confidence     float64
	occurrences    int
	aliases        map[string]bool
	sources        map[string]string // articleID -> title
	firstSeen      time.Time
	lastSeen       time.Time
}

// Discover reads unprocessed articles since opts.Since (bounded by
// opts.Limit), sends them to llm in batches of opts.BatchSize, discards
// any entity with no textual evidence in its own batch, classifies each
// survivor against catalog and against every other candidate discovered
// in this same run, aggregates occurrences/aliases/sources, and upserts
// the result via repo. A batch that errors (LLM unreachable, invalid
// JSON) is counted in BatchesFailed and skipped — its articles are left
// unmarked so a later run retries them — but never aborts the rest of
// the run. Discover itself returns a non-nil error only for an
// infrastructure failure reading articles or checking/marking processed
// state, never for an LLM/batch failure.
func Discover(ctx context.Context, articles ArticleLister, llm appout.EntityDiscoveryLLM, repo entityproposalport.EntityProposalRepository, catalog CatalogIndex, opts DiscoverOptions, now time.Time, verifier Verifier) (DiscoverResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	var pending []article.Article
	var err error
	if opts.Force {
		pending, err = articles.ListSince(ctx, opts.Since, limit)
	} else {
		pending, err = articles.ListUnprocessedByEntityDiscovery(ctx, opts.Since, opts.ExtractorVersion, limit)
	}
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("entitydiscovery: list articles: %w", err)
	}

	result := DiscoverResult{ArticlesConsidered: len(pending)}
	aggregates := map[string]*aggregate{}
	surnameOwnerThisRun := map[string]string{} // normalized surname -> aggregate key, for kind=new_entity only

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	for start := 0; start < len(pending); start += batchSize {
		end := start + batchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		result.BatchesTotal++

		llmArticles := make([]appout.EntityDiscoveryArticle, len(batch))
		for i, a := range batch {
			llmArticles[i] = appout.EntityDiscoveryArticle{ArticleID: a.ID, Title: a.Title, Summary: a.Summary}
		}

		res, err := llm.Discover(ctx, llmArticles)
		if err != nil {
			result.BatchesFailed++
			articleIDs := make([]string, len(batch))
			for i, a := range batch {
				articleIDs[i] = a.ID
			}
			result.BatchErrors = append(result.BatchErrors, fmt.Sprintf(
				"batch %d (articles: %s): %v",
				result.BatchesTotal, strings.Join(articleIDs, ","), err,
			))
			continue
		}

		for _, e := range res.Entities {
			if !hasEvidence(e.Name, e.Aliases, batch) {
				continue
			}
			et := entityproposal.EntityType(strings.ToLower(e.Type))
			if !validEntityType(et) {
				continue
			}
			if opts.League != nil {
				if noisy, _ := leaguegazetteer.IsNoise(e.Name, opts.League.MinLetters()); noisy {
					result.SkippedNoise++
					continue
				}
				if leagueKnown(opts.League, e.Name, et, batch) {
					result.AlreadyKnownLeague++
					continue
				}
			}
			if _, ok := catalog.Lookup(entityproposal.Normalize(e.Name)); ok {
				result.Verification.CatalogHits++
			}
			classifyAndMerge(aggregates, surnameOwnerThisRun, catalog, e, et, batch, now)
		}

		articleIDs := make([]string, len(batch))
		for i, a := range batch {
			articleIDs[i] = a.ID
		}
		if err := repo.MarkArticlesProcessed(ctx, opts.ExtractorVersion, articleIDs, now); err != nil {
			return result, fmt.Errorf("entitydiscovery: mark articles processed: %w", err)
		}
	}

	result.Verification.CandidatesConsidered = len(aggregates)
	knownTeamIDs := catalog.TeamIDs()
	for _, agg := range aggregates {
		p := entityproposal.Proposal{
			CanonicalName:    agg.canonicalName,
			NormalizedName:   agg.normalizedName,
			EntityType:       agg.entityType,
			Kind:             agg.kind,
			TargetEntityID:   agg.targetEntityID,
			Metadata:         agg.metadata,
			Confidence:       agg.confidence,
			Occurrences:      agg.occurrences,
			Status:           entityproposal.StatusPending,
			FirstSeen:        agg.firstSeen,
			LastSeen:         agg.lastSeen,
			ExtractorVersion: opts.ExtractorVersion,
			CreatedAt:        now,
		}
		for a := range agg.aliases {
			p.Aliases = append(p.Aliases, a)
		}
		for articleID, title := range agg.sources {
			p.Sources = append(p.Sources, entityproposal.Source{ArticleID: articleID, ArticleTitle: title})
		}

		if verifier == nil || !ShouldVerify(p, opts.VerifyMinConfidence) {
			if _, err := repo.Upsert(ctx, p); err != nil {
				return result, fmt.Errorf("entitydiscovery: upsert proposal %q: %w", p.CanonicalName, err)
			}
			result.ProposalsUpserted++
			continue
		}

		// Upsert the plain (pre-verification) proposal first: it merges
		// into any existing pending row and yields the row id. The merged
		// row (earlier runs' aliases, metadata, confidence) is what the
		// verification outcome is applied to, so UpdateVerification's
		// wholesale replace cannot lose accumulated state.
		id, err := repo.Upsert(ctx, p)
		if err != nil {
			return result, fmt.Errorf("entitydiscovery: upsert proposal %q: %w", p.CanonicalName, err)
		}
		result.ProposalsUpserted++

		merged, err := repo.Get(ctx, id)
		if err != nil {
			return result, fmt.Errorf("entitydiscovery: get proposal %q: %w", p.CanonicalName, err)
		}
		if merged.Status != entityproposal.StatusPending {
			// Already decided (e.g. auto-rejected earlier): never re-verify.
			continue
		}

		out, err := verifyAndPersist(ctx, repo, verifier, nil, catalog, knownTeamIDs, p, merged, false, attempt{})
		if err != nil {
			return result, err
		}
		switch out {
		case outcomeAutoRejected:
			result.AutoRejected++
		case outcomeUncertain, outcomeConflict:
			result.Uncertain++
		case outcomeDuplicate:
			result.Duplicates++
		}
	}

	result.Verification.LeftForReview = result.Uncertain
	if verifier != nil {
		if sr, ok := verifier.(StatsReporter); ok {
			result.Verification.Chain = sr.ChainStats()
		}
	}
	return result, nil
}

func validEntityType(et entityproposal.EntityType) bool {
	switch et {
	case entityproposal.EntityTypePlayer, entityproposal.EntityTypeCoach, entityproposal.EntityTypeExecutive, entityproposal.EntityTypeTeam:
		return true
	default:
		return false
	}
}

// hasEvidence reports whether name or any of aliases appears, once both
// sides are run through entityproposal.Normalize, as a substring of at
// least one batch article's title+summary — the hallucination guard.
func hasEvidence(name string, aliases []string, batch []article.Article) bool {
	candidates := append([]string{name}, aliases...)
	for _, a := range batch {
		haystack := entityproposal.Normalize(a.Title + " " + a.Summary)
		for _, c := range candidates {
			nc := entityproposal.Normalize(c)
			if nc != "" && strings.Contains(haystack, nc) {
				return true
			}
		}
	}
	return false
}

// leagueKnown reports whether a mention already resolves, with the batch
// articles that contain it as corroboration, to a league player (Resolved) or
// an NFL team alias.
func leagueKnown(idx *leaguegazetteer.Index, name string, et entityproposal.EntityType, batch []article.Article) bool {
	switch et {
	case entityproposal.EntityTypeTeam:
		return idx.ResolveTeam(name).Status == leaguegazetteer.TeamResolved
	case entityproposal.EntityTypePlayer:
		norm := entityproposal.Normalize(name)
		var parts []string
		for _, a := range batch {
			if h := entityproposal.Normalize(a.Title + " " + a.Summary); strings.Contains(h, norm) {
				parts = append(parts, a.Title, a.Summary)
			}
		}
		ctx := leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText(parts...)}
		return idx.ResolvePlayer(name, ctx).Status == leaguegazetteer.Resolved
	}
	return false
}

// classifyAndMerge decides e's Kind (and target, for an alias) against
// catalog and against every aggregate already built in this run, then
// merges e into the resulting aggregate (creating one if this is the
// first occurrence of its key).
func classifyAndMerge(aggregates map[string]*aggregate, surnameOwnerThisRun map[string]string, catalog CatalogIndex, e appout.EntityDiscoveryEntity, et entityproposal.EntityType, batch []article.Article, now time.Time) {
	normalized := entityproposal.Normalize(e.Name)
	if normalized == "" {
		return
	}

	if _, ok := catalog.Lookup(normalized); ok {
		// Exact match to a known canonical name/alias: nothing to
		// propose (see design spec §4.7 — observed-alias-on-a-known-
		// entity evidence recording is intentionally out of scope for
		// this version).
		return
	}

	surname := entityproposal.Surname(e.Name)

	if surname != "" {
		if owner, ok := catalog.SurnameOwner(surname); ok {
			mergeAliasAggregate(aggregates, owner, e, et, batch, now)
			return
		}
	}

	key := normalized + "\x00" + string(et)

	if surname != "" {
		if existingKey, collides := surnameOwnerThisRun[surname]; collides && existingKey != key {
			// Two different brand-new candidates in this run share a
			// surname: flag both, never merge them silently.
			if existing, ok := aggregates[existingKey]; ok {
				existing.kind = entityproposal.KindPossibleDuplicate
			}
			mergeNewEntityAggregate(aggregates, key, entityproposal.KindPossibleDuplicate, e, et, normalized, batch, now)
			return
		}
		surnameOwnerThisRun[surname] = key
	}

	kind := entityproposal.KindNewEntity
	if existing, ok := aggregates[key]; ok {
		kind = existing.kind // preserve a possible_duplicate flag already set on this key
	}
	mergeNewEntityAggregate(aggregates, key, kind, e, et, normalized, batch, now)
}

func mergeNewEntityAggregate(aggregates map[string]*aggregate, key string, kind entityproposal.Kind, e appout.EntityDiscoveryEntity, et entityproposal.EntityType, normalized string, batch []article.Article, now time.Time) {
	agg, ok := aggregates[key]
	if !ok {
		agg = &aggregate{
			canonicalName: e.Name, normalizedName: normalized, entityType: et, kind: kind,
			metadata: map[string]string{}, aliases: map[string]bool{}, sources: map[string]string{},
			firstSeen: now, lastSeen: now,
		}
		aggregates[key] = agg
	}
	agg.kind = kind
	mergeOccurrence(agg, e, batch)
}

func mergeAliasAggregate(aggregates map[string]*aggregate, owner CatalogEntity, e appout.EntityDiscoveryEntity, et entityproposal.EntityType, batch []article.Article, now time.Time) {
	key := "alias:" + owner.ID
	agg, ok := aggregates[key]
	if !ok {
		agg = &aggregate{
			canonicalName: owner.CanonicalName, normalizedName: entityproposal.Normalize(owner.CanonicalName),
			entityType: owner.Type, kind: entityproposal.KindNewAlias, targetEntityID: owner.ID,
			metadata: map[string]string{}, aliases: map[string]bool{}, sources: map[string]string{},
			firstSeen: now, lastSeen: now,
		}
		aggregates[key] = agg
	}
	agg.aliases[e.Name] = true
	mergeOccurrence(agg, e, batch)
}

func mergeOccurrence(agg *aggregate, e appout.EntityDiscoveryEntity, batch []article.Article) {
	agg.occurrences++
	if e.Confidence > agg.confidence {
		agg.confidence = e.Confidence
	}
	for _, a := range e.Aliases {
		agg.aliases[a] = true
	}
	for k, v := range e.Metadata {
		if _, exists := agg.metadata[k]; !exists && v != "" {
			agg.metadata[k] = v
		}
	}
	for _, a := range batch {
		haystack := entityproposal.Normalize(a.Title + " " + a.Summary)
		if strings.Contains(haystack, entityproposal.Normalize(e.Name)) {
			agg.sources[a.ID] = a.Title
		}
	}
}
