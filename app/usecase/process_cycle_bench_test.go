package usecase_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// benchCascade builds a DedupCascadeConfig representative of production
// (configs/settings.yml): dedup + clustering enabled (defaultCascade()
// already does this — see the top of process_cycle_test.go), plus
// classification enabled (settings.yml: classification.enabled: true) and
// editorial in "shadow" mode (settings.yml: editorial.mode: shadow — never
// gates, but does score+record every article), so the benchmark exercises
// every step process_cycle.go's doc comment lists: normalize -> dedup ->
// classify -> near-dup scoring -> cluster -> editorial score -> freshness ->
// accept/reject.
func benchCascade() in.DedupCascadeConfig {
	cascade := defaultCascade()
	cascade.Classification = in.ClassificationSettings{Enabled: true, TrustedSourceIDs: map[string]bool{}}
	cascade.Editorial = in.EditorialSettings{Mode: "shadow"}
	return cascade
}

// benchRawArticles builds n synthetic raw article.Article fetch results —
// the shape fakeFetcher.Fetch returns, matching the fixture pattern used
// throughout process_cycle_test.go (e.g. TestProcessCycle_AcceptsFresh
// ArticleAndEnqueuesForPublication). Every article:
//   - carries "Springfield Isotopes" prominently in its title, so
//     benchClassificationMatcher's StrongOverride official_names entry
//     (Weight 12, over PositiveThresholdHigh=10 even before the
//     TitleWeightMultiplier) reliably classifies it as related — most
//     fetched articles from this bot's curated sources are Isotopes
//     articles in production, so almost all 50 should reach the
//     clustering/editorial/freshness/accept steps, not bail out early at
//     classification.
//   - has a distinct OriginalURL/ExternalID (index-suffixed) so none of
//     the 50 collide with each other on canonical-URL fingerprint or
//     external-id/url/content-fingerprint/attribute-combo alias within a
//     single iteration.
//   - is published within the last hour (relative to now), well inside
//     freshness's 72h HardNewsHours window, so freshness never rejects it.
//   - mentions one of two players and one of four clustering.Rules action
//     terms (sign/release/injury/confirmed) from clusterTestSettings, so
//     eventsignals.Extract has real entities/actions to find rather than
//     running over inert text.
func benchRawArticles(n int, now time.Time) []article.Article {
	players := []string{"Jane Doe", "John Smith"}
	actions := []string{"sign", "release", "injury", "confirmed"}
	articles := make([]article.Article, n)
	for i := 0; i < n; i++ {
		player := players[i%len(players)]
		action := actions[i%len(actions)]
		// bench-source-N is a deterministic, always-valid TypeWeb slug, so a
		// NewID failure here can only mean this benchmark fixture itself is
		// broken — panic rather than thread a *testing.T/B through a plain
		// data-builder helper for an error path that can never fire.
		sourceID, err := source.NewID(source.TypeWeb, fmt.Sprintf("bench-source-%d", i%5))
		if err != nil {
			panic(fmt.Sprintf("benchRawArticles: %v", err))
		}
		articles[i] = article.Article{
			ExternalID:  fmt.Sprintf("bench-ext-%d", i),
			OriginalURL: fmt.Sprintf("https://example.com/bench/article-%d", i),
			Source: source.Source{
				ID:     sourceID,
				Name:   "Bench Source",
				Type:   source.TypeWeb,
				Domain: "example.com",
				URL:    "https://example.com",
			},
			Title: fmt.Sprintf("Springfield Isotopes %s %s in latest roster update #%d", action, player, i),
			Summary: fmt.Sprintf(
				"The Springfield Isotopes confirmed a move involving %s today, according to team sources close to "+
					"the organization. This is update number %d for the week.", player, i,
			),
			Author:      "Bench Reporter",
			Language:    "en",
			PublishedAt: now.Add(-time.Duration(i) * time.Minute),
			ContentType: article.ContentTypeArticle,
		}
	}
	return articles
}

// benchHarness pairs a *usecase.ProcessCycle (embedded, so h.Run promotes
// straight through) with the fixed inputs (raw fetch batch, cascade config,
// classification matcher, editorial model, clock) needed to rebuild it.
//
// The fixed inputs are built once in newProcessCycleForBench and never
// touched again — they are read-only per Run() (fakeFetcher.Fetch returns
// h.raw verbatim, and process_cycle.go's article.Normalize copies before
// mutating, so raw article.Article values are never written back into
// h.raw). Only resetFakeState changes between iterations, and it only ever
// replaces the *stateful* fakes (repositories/outbox/unit-of-work) — see
// its own doc comment for why that is what makes the reset genuine.
type benchHarness struct {
	*usecase.ProcessCycle
	raw     []article.Article
	cascade in.DedupCascadeConfig
	matcher dictionary.Matcher
	model   *editorial.Model
	clock   fakeClock
}

// newProcessCycleForBench adapts process_cycle_test.go's newProcessCycle
// constructor pattern (see that function, and newProcessCycleWithClassification/
// newProcessCycleWithEditorial) to *testing.B and a batch size: it builds the
// same real fake types those constructors use, wires them through the same
// exported usecase.NewProcessCycle constructor, but exposes every stateful
// fake so resetFakeState can rebuild them between b.N iterations.
func newProcessCycleForBench(b *testing.B, n int) *benchHarness {
	b.Helper()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	h := &benchHarness{
		raw:     benchRawArticles(n, now),
		cascade: benchCascade(),
		matcher: classificationMatcher(b),
		model:   alwaysAmbiguousModel(),
		clock:   fakeClock{now: now},
	}
	h.resetFakeState()
	return h
}

// resetFakeState rebuilds every *stateful* fake (article repo, dedup repo,
// alias repo, similarity index repo, cluster repo, decision repo, outbox
// repo, unit of work) from scratch and wires a brand-new *usecase.ProcessCycle
// around them — the same "construct fresh fakes per run" pattern every test
// in process_cycle_test.go already uses per-test, just invoked once per
// benchmark iteration instead of once per Test function.
//
// This matters because several of these fakes accumulate real, behavior-
// affecting state across calls, not just bookkeeping:
//   - fakeAliasRepository.byKey is written by Save (called from accept's
//     saveAliases) and read by FindArticleID (called from matchAlias). Since
//     every alias value matchAlias/saveAliases compute (external-id, url,
//     content-fingerprint, attribute-combo) is derived only from article
//     content — never from the fresh UUID article.ID processOne assigns —
//     the exact same 50-article raw batch produces the exact same alias
//     values on every iteration. A *fakeAliasRepository carried over from a
//     prior iteration would already have iteration 1's aliases saved, so on
//     iteration 2 matchAlias's very first check (or one of the first four)
//     would find a match and reject every article as an "exact duplicate"
//     before it ever reached classification/clustering/editorial/freshness —
//     silently turning the benchmark into "50 near-instant duplicate
//     rejects" from iteration 2 onward, which is exactly the class of
//     benchmark-realism bug this task's brief warns about.
//   - fakeArticleRepo/fakeClusterRepo/fakeDecisionRepo/fakeOutboxRepo/
//     fakeDeduplicationRepo.saved all accumulate append-only slices/maps
//     across calls (r.saved, r.created, r.events, r.enqueued, ...). None of
//     those particular accumulated fields feed back into a decision
//     (fakeDeduplicationRepo.Exists compares only against the fixed
//     knownFingerprint field, never r.saved; fakeClusterRepo.
//     FindCandidatesByEventKeys and fakeSimilarityIndexRepository.
//     FindCandidates both return a fixed candidates field regardless of
//     what's been indexed/created), so leaving them unreset would not
//     change Run's control flow the way a stale alias map would — but it
//     would let unbounded slices/maps grow for the lifetime of the
//     benchmark, understating steady-state per-cycle allocation cost
//     (b.ReportAllocs) and making later iterations pay ever-growing
//     append/GC costs unrelated to what ProcessCycle.Run itself does per
//     cycle. Rebuilding every fake fresh avoids both problems at once
//     rather than special-casing only the alias repo.
//   - fakeUnitOfWork is stateless (err is always nil here) but is rebuilt
//     too, for the same "every fake fresh, nothing to reason about" reason.
//
// h.raw, h.cascade, h.matcher, h.model, and h.clock are configuration/input,
// not state ProcessCycle.Run mutates, so they are reused as-is across every
// call — only the fakes and the *usecase.ProcessCycle wired around them are
// replaced.
func (h *benchHarness) resetFakeState() {
	h.ProcessCycle = usecase.NewProcessCycle(
		fakeFetcher{articles: h.raw},
		&fakeArticleRepo{},
		&fakeDeduplicationRepo{},
		&fakeAliasRepository{},
		&fakeSimilarityIndexRepository{},
		&fakeClusterRepo{},
		&fakeDecisionRepo{},
		&fakeOutboxRepo{},
		&fakeUnitOfWork{},
		freshness,
		h.matcher,
		h.model,
		nil,
		h.cascade,
		h.clock,
		fakeLogger{},
		&fakeSourcePolicyRepository{},
		&fakeSourcePolicyResolver{},
		&fakeClusterPublicationRepository{},
		&fakeSeenItemsRepository{},
		&fakeSourcePolicyCooldownRepository{},
		&fakeUnknownEntityRepository{},
		nil,
	)
}

// BenchmarkProcessCycle_Run_50Articles exercises one full cycle
// (normalize -> exact dedup -> classify -> near-dup scoring -> cluster ->
// editorial score -> freshness -> accept/reject) over a batch size
// representative of one scheduler tick across all configured sources
// (5 websites + RSS + social accounts, each capped at max_items_per_cycle —
// see configs/sources.yml).
//
// -benchtime=20x is required (not the default adaptive benchtime): each
// iteration does real work across ~10 fake repositories for 50 articles,
// so adaptive scaling could run for a very long time.
func BenchmarkProcessCycle_Run_50Articles(b *testing.B) {
	h := newProcessCycleForBench(b, 50)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		h.resetFakeState()
		b.StartTimer()
		if _, err := h.Run(ctx); err != nil {
			b.Fatalf("Run: %v", err)
		}
	}
}
