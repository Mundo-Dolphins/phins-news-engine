package entityresolution_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newTestDB opens a fresh SQLite database file and applies every embedded
// migration — same helper pattern
// (adapters/outbound/sqliteentityproposal's own tests) reuses,
// via infrasqlite.Open + infrasqlite.Migrate against migrations.FS,
// without that package's template-copy optimization since this suite has
// only a handful of tests.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

// fakeLogger records every Info-level message (by its first argument,
// the constant message string) so tests can assert which observability
// log lines fired, without asserting on structured field values.
type fakeLogger struct {
	infoMessages []string
}

func (*fakeLogger) Debug(string, ...any) {}
func (f *fakeLogger) Info(msg string, _ ...any) {
	f.infoMessages = append(f.infoMessages, msg)
}
func (*fakeLogger) Warn(string, ...any)         {}
func (*fakeLogger) Error(string, error, ...any) {}

type fakeResolutionLLM struct {
	calls   int
	results map[string]out.EntityResolutionResult // keyed by candidate.Text
	err     error
}

func (f *fakeResolutionLLM) Resolve(ctx context.Context, c out.EntityResolutionCandidate) (out.EntityResolutionResult, error) {
	f.calls++
	if f.err != nil {
		return out.EntityResolutionResult{}, f.err
	}
	return f.results[c.Text], nil
}

func TestResolve_SuccessfulResolutionIsReturnedAndCached(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"}}
	articleText := map[string]string{"title": "Isotopes sign Random Newcomer"}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())

	if len(resolved) != 1 || resolved[0].CanonicalName != "Random Newcomer" || resolved[0].Type != "player" {
		t.Fatalf("Resolve() = %+v, want one resolved player", resolved)
	}
	if llm.calls != 1 {
		t.Fatalf("llm.calls = %d, want 1", llm.calls)
	}

	// Second call, same candidate: served from cache, no second LLM call.
	resolved2 := entityresolution.Resolve(context.Background(), "article-2", unknowns, articleText, deps, time.Now())
	if len(resolved2) != 1 || resolved2[0].CanonicalName != "Random Newcomer" {
		t.Fatalf("Resolve() cached = %+v, want one resolved player", resolved2)
	}
	if llm.calls != 1 {
		t.Fatalf("llm.calls after cached lookup = %d, want still 1", llm.calls)
	}
}

// TestResolve_SecondOccurrenceOfSameRawSpanHitsCacheViaAlias covers
// Finding 1 of the 2026-09-16 final review: when the candidate span
// differs from the LLM's canonical name (e.g. "Sign Random Newcomer" ->
// "Random Newcomer"), the proposal is persisted under the canonical
// name's normalized form, so a later occurrence of the SAME raw span
// must hit the cache via the alias persistEvidence records for it —
// not fall through and call the LLM again.
func TestResolve_SecondOccurrenceOfSameRawSpanHitsCacheViaAlias(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Sign Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{{Text: "Sign Random Newcomer", NormalizedText: "sign random newcomer", Field: "title"}}
	articleText := map[string]string{"title": "Isotopes Sign Random Newcomer"}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())
	if len(resolved) != 1 || resolved[0].CanonicalName != "Random Newcomer" {
		t.Fatalf("Resolve() = %+v, want one resolved player named Random Newcomer", resolved)
	}
	if llm.calls != 1 {
		t.Fatalf("llm.calls = %d, want 1", llm.calls)
	}

	// Second call, DIFFERENT article, SAME raw candidate text/NormalizedText:
	// must be served from the cache via the alias path, not a second LLM call.
	resolved2 := entityresolution.Resolve(context.Background(), "article-2", unknowns, articleText, deps, time.Now())
	if len(resolved2) != 1 || resolved2[0].CanonicalName != "Random Newcomer" {
		t.Fatalf("Resolve() second call = %+v, want one resolved player named Random Newcomer", resolved2)
	}
	if llm.calls != 1 {
		t.Fatalf("llm.calls after second occurrence of same raw span = %d, want still 1 (cache hit via alias)", llm.calls)
	}
}

func TestResolve_DedupesSameNormalizedCandidateAcrossFields(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{
		{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"},
		{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "body"},
	}
	articleText := map[string]string{"title": "Isotopes sign Random Newcomer", "body": "Random Newcomer joins the roster."}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())

	if len(resolved) != 1 {
		t.Fatalf("Resolve() = %d entries, want 1 (deduped)", len(resolved))
	}
	if llm.calls != 1 {
		t.Fatalf("llm.calls = %d, want 1 (one call per distinct normalized candidate)", llm.calls)
	}
}

func TestResolve_LLMErrorDegradesToUnresolvedWithoutPanicking(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{err: context.DeadlineExceeded}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"}}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, map[string]string{"title": "x"}, deps, time.Now())

	if len(resolved) != 0 {
		t.Fatalf("Resolve() = %+v, want empty on LLM error", resolved)
	}
}

func TestResolve_ValidationFailureIsDiscarded(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Tom Bront", Confidence: 0.95}, // unrelated name
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"}}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, map[string]string{"title": "x"}, deps, time.Now())

	if len(resolved) != 0 {
		t.Fatalf("Resolve() = %+v, want empty (hallucination guard should reject Tom Bront)", resolved)
	}
}

// TestResolve_CacheHitValidation covers Finding 2 of the 2026-09-16 final
// review: a cache hit must not be trusted unconditionally — a REJECTED
// proposal, one below the fallback's confidence floor, or one that isn't
// Kind == KindNewEntity must all be treated as a cache MISS (fall through
// to the LLM), while a normal approved/pending, sufficiently confident,
// KindNewEntity proposal must still be served from the cache as before.
func TestResolve_CacheHitValidation(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	const normalizedText = "random newcomer"

	tests := []struct {
		name           string
		proposal       entityproposal.Proposal
		wantLLMCalls   int
		wantResolvedTo string // "" means unresolved
	}{
		{
			name: "rejected proposal is not reused",
			proposal: entityproposal.Proposal{
				CanonicalName: "Random Newcomer", NormalizedName: normalizedText,
				EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
				Confidence: 0.9, Status: entityproposal.StatusRejected,
			},
			wantLLMCalls:   1,
			wantResolvedTo: "Random Newcomer", // the LLM call itself resolves it
		},
		{
			name: "below confidence floor is not reused",
			proposal: entityproposal.Proposal{
				CanonicalName: "Random Newcomer", NormalizedName: normalizedText,
				EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
				Confidence: 0.1, Status: entityproposal.StatusApproved,
			},
			wantLLMCalls:   1,
			wantResolvedTo: "Random Newcomer",
		},
		{
			name: "non-new-entity kind is not reused",
			proposal: entityproposal.Proposal{
				CanonicalName: "Random Newcomer", NormalizedName: normalizedText,
				EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewAlias,
				Confidence: 0.9, Status: entityproposal.StatusApproved,
			},
			wantLLMCalls:   1,
			wantResolvedTo: "Random Newcomer",
		},
		{
			name: "approved, confident, new-entity IS reused",
			proposal: entityproposal.Proposal{
				CanonicalName: "Random Newcomer", NormalizedName: normalizedText,
				EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
				Confidence: 0.9, Status: entityproposal.StatusApproved,
			},
			wantLLMCalls:   0,
			wantResolvedTo: "Random Newcomer",
		},
		{
			name: "pending, confident, new-entity IS reused",
			proposal: entityproposal.Proposal{
				CanonicalName: "Random Newcomer", NormalizedName: normalizedText,
				EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
				Confidence: 0.9, Status: entityproposal.StatusPending,
			},
			wantLLMCalls:   0,
			wantResolvedTo: "Random Newcomer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			cache := sqliteentityproposal.NewEntityProposalRepository(db)
			tc.proposal.ID = "seed-1"
			tc.proposal.Metadata = map[string]string{}
			tc.proposal.FirstSeen, tc.proposal.LastSeen, tc.proposal.CreatedAt = now, now, now
			tc.proposal.ExtractorVersion = "v1"
			if _, err := cache.Upsert(context.Background(), tc.proposal); err != nil {
				t.Fatalf("seed Upsert() error = %v", err)
			}
			// Upsert always inserts as StatusPending; force the row to the
			// status this case actually needs (rejected/approved), since
			// Upsert has no path to create a non-pending row directly.
			if tc.proposal.Status != entityproposal.StatusPending {
				if _, err := db.Exec(`UPDATE entity_proposals SET status = ? WHERE id = ?`, string(tc.proposal.Status), tc.proposal.ID); err != nil {
					t.Fatalf("force status: %v", err)
				}
			}

			llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
				"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
			}}
			deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.5, Logger: &fakeLogger{}}
			unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: normalizedText, Field: "title"}}
			articleText := map[string]string{"title": "Isotopes sign Random Newcomer"}

			resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, now)

			if llm.calls != tc.wantLLMCalls {
				t.Errorf("llm.calls = %d, want %d", llm.calls, tc.wantLLMCalls)
			}
			gotResolvedTo := ""
			if len(resolved) == 1 {
				gotResolvedTo = resolved[0].CanonicalName
			}
			if gotResolvedTo != tc.wantResolvedTo {
				t.Errorf("resolved = %+v, want CanonicalName %q", resolved, tc.wantResolvedTo)
			}
		})
	}
}

// TestResolve_MaxCandidatesPerArticleCapsLLMCalls covers Finding 4 of the
// 2026-09-16 final review: Resolve must not spend unbounded per-article
// time calling the LLM once per distinct unknown-entity candidate — when
// MaxCandidatesPerArticle is set, only that many distinct candidates are
// processed and the rest are silently skipped (left unresolved).
func TestResolve_MaxCandidatesPerArticleCapsLLMCalls(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Candidate One":   {Resolved: true, Type: "player", CanonicalName: "Candidate One", Confidence: 0.9},
		"Candidate Two":   {Resolved: true, Type: "player", CanonicalName: "Candidate Two", Confidence: 0.9},
		"Candidate Three": {Resolved: true, Type: "player", CanonicalName: "Candidate Three", Confidence: 0.9},
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.5, Logger: &fakeLogger{}, MaxCandidatesPerArticle: 2}
	unknowns := []eventsignals.UnknownEntity{
		{Text: "Candidate One", NormalizedText: "candidate one", Field: "title"},
		{Text: "Candidate Two", NormalizedText: "candidate two", Field: "title"},
		{Text: "Candidate Three", NormalizedText: "candidate three", Field: "title"},
	}
	articleText := map[string]string{"title": "x"}

	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())

	if llm.calls != 2 {
		t.Fatalf("llm.calls = %d, want 2 (capped by MaxCandidatesPerArticle)", llm.calls)
	}
	if len(resolved) != 2 {
		t.Fatalf("len(resolved) = %d, want 2", len(resolved))
	}
}

func TestResolve_NoCandidatesNeverCallsLLM(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}

	resolved := entityresolution.Resolve(context.Background(), "article-1", nil, nil, deps, time.Now())

	if len(resolved) != 0 || llm.calls != 0 {
		t.Fatalf("Resolve() with no candidates: resolved=%+v calls=%d, want empty/0", resolved, llm.calls)
	}
}

func TestResolve_LogsTriggeredWhenCandidatesPresent(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
	}}
	logger := &fakeLogger{}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: logger}
	unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"}}
	articleText := map[string]string{"title": "Isotopes sign Random Newcomer"}

	entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())

	if !containsMessage(logger.infoMessages, "entity resolution fallback: triggered") {
		t.Fatalf("infoMessages = %v, want a \"triggered\" log line", logger.infoMessages)
	}
	if !containsMessage(logger.infoMessages, "entity resolution fallback: resolution accepted") {
		t.Fatalf("infoMessages = %v, want a \"resolution accepted\" log line", logger.infoMessages)
	}
}

// TestResolve_DeadlineReturnsWhenResolverIgnoresContext is the operational
// safety net for the ingestion path: a misbehaving provider client must not
// be able to hold the per-article SQLite transaction forever.
func TestResolve_DeadlineReturnsWhenResolverIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	llm := &blockingResolutionLLM{release: release}
	deps := entityresolution.Deps{
		LLM: llm, Cache: sqliteentityproposal.NewEntityProposalRepository(newTestDB(t)), MinConfidence: 0.7,
		Logger: &fakeLogger{}, Timeout: 20 * time.Millisecond,
	}
	unknowns := []eventsignals.UnknownEntity{{Text: "Blocked Candidate", NormalizedText: "blocked candidate", Field: "title"}}

	started := time.Now()
	resolved := entityresolution.Resolve(context.Background(), "article-1", unknowns, map[string]string{"title": "Blocked Candidate"}, deps, time.Now())
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Resolve() took %v, want it to fail open at the configured deadline", elapsed)
	}
	if len(resolved) != 0 {
		t.Fatalf("Resolve() = %+v, want no resolution after deadline", resolved)
	}
}

type blockingResolutionLLM struct{ release <-chan struct{} }

func (b *blockingResolutionLLM) Resolve(context.Context, out.EntityResolutionCandidate) (out.EntityResolutionResult, error) {
	<-b.release // deliberately ignores ctx.Done()
	return out.EntityResolutionResult{}, nil
}

func TestResolve_LogsCacheHitOnSecondLookup(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{results: map[string]out.EntityResolutionResult{
		"Random Newcomer": {Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
	}}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: &fakeLogger{}}
	unknowns := []eventsignals.UnknownEntity{{Text: "Random Newcomer", NormalizedText: "random newcomer", Field: "title"}}
	articleText := map[string]string{"title": "Isotopes sign Random Newcomer"}
	entityresolution.Resolve(context.Background(), "article-1", unknowns, articleText, deps, time.Now())

	logger2 := &fakeLogger{}
	deps2 := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: logger2}
	entityresolution.Resolve(context.Background(), "article-2", unknowns, articleText, deps2, time.Now())

	if !containsMessage(logger2.infoMessages, "entity resolution fallback: cache hit") {
		t.Fatalf("infoMessages = %v, want a \"cache hit\" log line on the second article", logger2.infoMessages)
	}
}

func TestResolve_NoCandidatesLogsNothing(t *testing.T) {
	db := newTestDB(t)
	cache := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := &fakeResolutionLLM{}
	logger := &fakeLogger{}
	deps := entityresolution.Deps{LLM: llm, Cache: cache, MinConfidence: 0.7, Logger: logger}

	entityresolution.Resolve(context.Background(), "article-1", nil, nil, deps, time.Now())

	if len(logger.infoMessages) != 0 {
		t.Fatalf("infoMessages = %v, want none when there are no candidates", logger.infoMessages)
	}
}

func containsMessage(messages []string, want string) bool {
	for _, m := range messages {
		if m == want {
			return true
		}
	}
	return false
}
