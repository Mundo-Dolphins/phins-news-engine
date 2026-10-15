package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var freshness = publishing.FreshnessPolicy{HardNewsHours: 72, SocialHours: 24, AnalysisHours: 168, DefaultHours: 72}

// recordingLogger wraps fakeLogger to additionally record every Info call
// (message plus kv pairs), for tests asserting on the audit-trail log
// lines cluster() emits when the entity resolution fallback changes an
// article's representation (Finding 5 of the 2026-09-16 final review).
// Kept separate from fakeLogger — used as a bare value literal
// (fakeLogger{}) at dozens of call sites in this package — rather than
// adding recording state to it directly.
type recordingLogger struct {
	fakeLogger
	infoCalls *[]recordedLogCall
}

type recordedLogCall struct {
	msg string
	kv  []any
}

func newRecordingLogger() recordingLogger {
	return recordingLogger{infoCalls: &[]recordedLogCall{}}
}

func (r recordingLogger) Info(msg string, kv ...any) {
	*r.infoCalls = append(*r.infoCalls, recordedLogCall{msg: msg, kv: kv})
}

func TestCanaryEnforced_Deterministic(t *testing.T) {
	id := "article-fixed-id-123"
	first := usecase.CanaryEnforced(id, 50)
	for i := 0; i < 100; i++ {
		if usecase.CanaryEnforced(id, 50) != first {
			t.Fatal("CanaryEnforced() is not deterministic for a fixed article ID and percent")
		}
	}
}

func TestCanaryEnforced_ZeroPercentNeverEnforces(t *testing.T) {
	for _, id := range []string{"a", "b", "c", "some-uuid-4"} {
		if usecase.CanaryEnforced(id, 0) {
			t.Errorf("CanaryEnforced(%q, 0) = true, want false", id)
		}
	}
}

func TestCanaryEnforced_HundredPercentAlwaysEnforces(t *testing.T) {
	for _, id := range []string{"a", "b", "c", "some-uuid-4"} {
		if !usecase.CanaryEnforced(id, 100) {
			t.Errorf("CanaryEnforced(%q, 100) = false, want true", id)
		}
	}
}

func TestCanaryEnforced_RoughlyMatchesConfiguredPercent(t *testing.T) {
	enforced := 0
	const n = 10000
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("article-%d", i)
		if usecase.CanaryEnforced(id, 10) {
			enforced++
		}
	}
	rate := float64(enforced) / n
	if rate < 0.07 || rate > 0.13 { // 10% target, generous tolerance for hash-bucket noise
		t.Errorf("enforced rate = %.3f over %d IDs, want roughly 0.10", rate, n)
	}
}

// clusterTestSettings builds a small but real event-clustering configuration
// — entities, action terms, content cues, event windows, DecideJoin
// thresholds/weights, and a representative-ranking config — exercising
// ProcessCycle's clustering step end-to-end (eventsignals.Extract through
// clustering.DecideJoin/ReplacesCurrent) without depending on the
// production configs/event_clustering_rules.yml file. Weights mirror that
// file's production ratios for the six clustering.Config signals; the
// RepresentativeConfig here is intentionally simplified (only Completeness/
// NoveltyWeight nonzero) so representative-switch tests can predict the
// exact score by hand.
func clusterTestSettings() in.ClusterSettings {
	return in.ClusterSettings{
		Enabled: true,
		Rules: eventsignals.Rules{
			Version: "test-cluster-rules-v1",
			ActionTerms: map[eventsignals.Action][]string{
				eventsignals.ActionSign:    {"sign", "signs", "signed"},
				eventsignals.ActionRelease: {"release", "released"},
				eventsignals.ActionInjure:  {"injury", "injured"},
				eventsignals.ActionConfirm: {"confirmed"},
			},
			ContentCues: map[eventsignals.ContentSignal][]string{
				eventsignals.ContentSignalConfirmation: {"confirmed", "officially"},
				eventsignals.ContentSignalRumor:        {"reportedly", "expected to"},
			},
			Entities: []eventsignals.EntityRule{
				{CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes", "springfield isotopes"}},
				{CanonicalName: "Shelbyville Sharks", Kind: eventsignals.EntityKindTeam, Terms: []string{"sharks", "shelbyville sharks"}},
				{CanonicalName: "Jane Doe", Kind: eventsignals.EntityKindPlayer, Terms: []string{"jane doe"}},
				{CanonicalName: "John Smith", Kind: eventsignals.EntityKindPlayer, Terms: []string{"john smith"}},
			},
			EventWindows: map[eventsignals.EventType]time.Duration{
				eventsignals.EventTypeTransaction:  48 * time.Hour,
				eventsignals.EventTypeInjury:       72 * time.Hour,
				eventsignals.EventTypeConfirmation: 24 * time.Hour,
				eventsignals.EventTypeRumor:        24 * time.Hour,
				eventsignals.EventTypeGeneric:      24 * time.Hour,
			},
		},
		Decide: clustering.Config{
			JoinThreshold: 0.72, JoinMargin: 0.08, AmbiguousLow: 0.45, AmbiguousHigh: 0.60,
			EntityMatchWeight: 0.35, ActionMatchWeight: 0.20, EventTypeMatchWeight: 0.15,
			OpponentMatchWeight: 0.10, DateWindowMatchWeight: 0.05, SourcePriorityWeight: 0.10,
			NoveltyMatchWeight: 0.05, RulesVersion: "test-cluster-rules-v1",
		},
		Representative: clustering.RepresentativeConfig{
			CompletenessWeight:         1,
			NoveltyWeight:              1,
			SubstantialUpdateThreshold: 0.3,
			RepresentativeSwitchMargin: 0.05,
			DefaultDomainPriority:      50,
			RulesVersion:               "test-cluster-rules-v1",
		},
		CandidateLimit: 50,
		RulesVersion:   "test-cluster-rules-v1",
	}
}

func defaultCascade() in.DedupCascadeConfig {
	return in.DedupCascadeConfig{
		Normalization: in.NormalizationSettings{
			URL:  normalization.URLConfig{TrailingSlash: "strip"},
			Text: normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3},
		},
		Dedup: in.DedupSettings{
			Enabled: true,
			Method:  "jaccard", DefaultThreshold: 0.82, ShortTextMaxTokens: 8, ShortTextThreshold: 0.90,
			WindowHours: 72, MaxCandidates: 50, MinSharedTerms: 2, FieldsUsed: []string{"title"},
			ContentTypeExceptions: map[article.ContentType]bool{}, AttributeComboWindow: 2 * time.Minute,
			RulesVersion: "dedup-rules-v1", EventThreshold: 0.55,
		},
		Cluster: clusterTestSettings(),
	}
}

func newProcessCycle(
	fetcher fakeFetcher, articles *fakeArticleRepo, dedup *fakeDeduplicationRepo, aliases *fakeAliasRepository,
	similarityIndex *fakeSimilarityIndexRepository, clusters *fakeClusterRepo, decisions *fakeDecisionRepo,
	outbox *fakeOutboxRepo, uow *fakeUnitOfWork, clock fakeClock,
) *usecase.ProcessCycle {
	return usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{}, nil)
}

// fakeUnknownEntityRepository is a hand-written test double for
// out.UnknownEntityRepository (no mocking framework in this codebase).
type fakeUnknownEntityRepository struct {
	calls int
	err   error
	// lastOccurrences is what the most recent SaveOccurrences call
	// received — used by tests that need to distinguish the raw,
	// pre-fallback-resolution UnknownEntities list (what SaveOccurrences
	// must always be given) from whatever eventsignals.Extract's
	// possible second, augmented pass reports.
	lastOccurrences []eventsignals.UnknownEntity
}

func (f *fakeUnknownEntityRepository) SaveOccurrences(ctx context.Context, articleID string, occurrences []eventsignals.UnknownEntity, rulesVersion string, now time.Time) error {
	f.calls++
	f.lastOccurrences = occurrences
	return f.err
}

// newProcessCycleWithSourcePolicy is newProcessCycle's counterpart for
// tests that need to control the source-policy resolver's Mode/policies
// directly (Task 10's Gate A tests) — every other call site keeps using
// newProcessCycle's always-inert &fakeSourcePolicyResolver{} default.
func newProcessCycleWithSourcePolicy(
	fetcher fakeFetcher, articles *fakeArticleRepo, dedup *fakeDeduplicationRepo, aliases *fakeAliasRepository,
	similarityIndex *fakeSimilarityIndexRepository, clusters *fakeClusterRepo, decisions *fakeDecisionRepo,
	outbox *fakeOutboxRepo, uow *fakeUnitOfWork, clock fakeClock, resolver *fakeSourcePolicyResolver,
) *usecase.ProcessCycle {
	return usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, &fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{}, nil)
}

// newProcessCycleWithSourcePolicyRepo is newProcessCycleWithSourcePolicy's
// counterpart for Gate B tests (Task 11) that also need to inspect what was
// persisted to the source-policy audit repository
// (out.SourcePolicyDecisionRepository) — Gate A only needed to control the
// resolver; Gate B also writes through the repository, so tests here take
// their own *fakeSourcePolicyRepository to assert against afterward.
func newProcessCycleWithSourcePolicyRepo(
	fetcher fakeFetcher, articles *fakeArticleRepo, dedup *fakeDeduplicationRepo, aliases *fakeAliasRepository,
	similarityIndex *fakeSimilarityIndexRepository, clusters *fakeClusterRepo, decisions *fakeDecisionRepo,
	outbox *fakeOutboxRepo, uow *fakeUnitOfWork, clock fakeClock, resolver *fakeSourcePolicyResolver, sourcePolicies *fakeSourcePolicyRepository,
) *usecase.ProcessCycle {
	return usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, sourcePolicies, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{}, nil)
}

// classificationMatcher builds a small Matcher exercising strong-override
// (team name), a gated ambiguous term, and a plain non-strong positive
// category — enough to test ProcessCycle's wiring without duplicating
// classification's own algorithm tests (see domain/classification).
//
// Takes testing.TB (not *testing.T) so process_cycle_bench_test.go's
// BenchmarkProcessCycle_Run_50Articles can reuse this fixture unchanged
// (via *testing.B) instead of duplicating the dictionary literal — the body
// only calls Helper()/Fatalf(), both part of testing.TB.
func classificationMatcher(t testing.TB) dictionary.Matcher {
	t.Helper()
	d := dictionary.Dictionary{
		Version: "test-classification-v1",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{
				{CanonicalName: "Springfield Isotopes", Source: "manual"},
			}},
			{Name: "nfl_context_terms", Weight: 3, Entries: []dictionary.Entry{
				{CanonicalName: "injury report", Source: "v1"},
			}},
			{
				Name: "ambiguous_team_terms", Weight: 4, RequiresAnyOf: []string{"nfl_context_terms", "official_names"},
				Entries: []dictionary.Entry{{CanonicalName: "isotopes", Source: "v1"}},
			},
		},
		Decision: dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3},
	}
	m, err := dictionary.Prepare(d)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return m
}

func newProcessCycleWithClassification(
	fetcher fakeFetcher, articles *fakeArticleRepo, decisions *fakeDecisionRepo, clusters *fakeClusterRepo,
	clock fakeClock, matcher dictionary.Matcher, trustedSourceIDs, contextSourceIDs map[string]bool,
) *usecase.ProcessCycle {
	cascade := defaultCascade()
	cascade.Classification = in.ClassificationSettings{
		Enabled: true, TrustedSourceIDs: trustedSourceIDs, ContextSourceIDs: contextSourceIDs,
	}
	return usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, decisions, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, matcher, nil, nil, cascade, clock, fakeLogger{},
		&fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)
}

func newProcessCycleWithEditorial(
	fetcher fakeFetcher, articles *fakeArticleRepo, decisions *fakeDecisionRepo, clusters *fakeClusterRepo,
	clock fakeClock, model *editorial.Model, mode string,
) *usecase.ProcessCycle {
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: mode}
	return usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, decisions, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, model, nil, cascade, clock, fakeLogger{},
		&fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)
}

// vetoingConfig returns an editorialveto.Config whose one rule triggers
// on the plain phrase "free bets" — deterministic and independent of any
// trained model's weights. A plain phrase (no regex metacharacters) is
// what editorialveto.Evaluate auto-wraps with `\b...\b` and matches
// case-insensitively (see its doc comment and domain/editorialveto's own
// tests); an explicit `\bfree bets\b` pattern would instead be compiled
// case-sensitively as-is and miss the test fixture's capitalized "Free
// bets" title.
func vetoingConfig() *editorialveto.Config {
	return &editorialveto.Config{
		Version: "test-veto-1",
		Rules: []editorialveto.Rule{
			{Category: editorialveto.CategoryGambling, ReasonCode: "veto:gambling:sportsbook_promo", Patterns: []string{"free bets"}},
		},
	}
}

func newProcessCycleWithEditorialAndVeto(
	fetcher fakeFetcher, articles *fakeArticleRepo, decisions *fakeDecisionRepo, clusters *fakeClusterRepo,
	clock fakeClock, model *editorial.Model, vetoCfg *editorialveto.Config, mode string,
) *usecase.ProcessCycle {
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: mode}
	return usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, decisions, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, model, vetoCfg, cascade, clock, fakeLogger{},
		&fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)
}

func TestProcessCycle_EnforceModeVetoRejectsRegardlessOfModelScore(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	// A model that would otherwise ACCEPT everything (Bias large positive,
	// no weights) — proves the veto, not the model, drove the rejection.
	alwaysAcceptModel := &editorial.Model{Version: "test-accept-v1", Bias: 5, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Free bets available for the big game", Summary: "Sign up now.",
		OriginalURL: "https://example.com/e1", ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	uc := newProcessCycleWithEditorialAndVeto(fetcher, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, alwaysAcceptModel, vetoingConfig(), "enforce")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want rejected=1 accepted=0 (veto overrides an accepting model)", result)
	}
	saved := articles.lastSaved()
	if saved.Status != article.StatusRejectedQuality {
		t.Errorf("Status = %q, want %q", saved.Status, article.StatusRejectedQuality)
	}
}

func TestProcessCycle_ShadowModeDeterministicVetoGates(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	alwaysAcceptModel := &editorial.Model{Version: "test-accept-v1", Bias: 5, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Free bets available for the big game", Summary: "Sign up now.",
		OriginalURL: "https://example.com/e1", ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	uc := newProcessCycleWithEditorialAndVeto(fetcher, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, alwaysAcceptModel, vetoingConfig(), "shadow")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Fatalf("result = %+v, want accepted=0 rejected=1 (deterministic vetoes are independent of model mode)", result)
	}
}

func TestProcessCycle_DeterministicVetoEvaluatedWhenEditorialDisabled(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Free bets available for the big game", Summary: "Sign up now.",
		OriginalURL: "https://example.com/e1", ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	uc := newProcessCycleWithEditorialAndVeto(fetcher, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, nil, vetoingConfig(), "disabled")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Fatalf("result = %+v, want accepted=0 rejected=1 (deterministic vetoes remain active)", result)
	}
}

// alwaysRejectModel scores every article into editorial.ZoneReject
// regardless of content — a deterministic fixture for exercising the
// enforce/shadow reject path without depending on feature extraction
// details already covered by domain/editorial's own tests. Bias is -9
// (rather than some large-magnitude value) so the resulting Value is a
// distinctly non-zero, non-negligible number (5.0 under Predict's rational
// squash, 50+50*(raw/(1+|raw|)) — see
// TestProcessCycle_EnforceModeRejectsInRejectZone) well clear of both zero
// and the Reject threshold (10), letting tests assert on it directly
// instead of just "!= 0".
func alwaysRejectModel() *editorial.Model {
	return &editorial.Model{Version: "test-reject-v1", Bias: -9, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
}

// alwaysAmbiguousModel scores every article into editorial.ZoneAmbiguous.
func alwaysAmbiguousModel() *editorial.Model {
	return &editorial.Model{Version: "test-ambiguous-v1", Bias: 0, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
}

func TestProcessCycle_EditorialDisabledNeverLoadsScoreAsPlaceholder(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Any article", OriginalURL: "https://example.com/e1",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	uc := newProcessCycleWithEditorial(fetcher, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, nil, "disabled")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0", result)
	}
	saved := articles.lastSaved()
	if saved.EditorialScore != editorial.NeutralScore {
		t.Errorf("EditorialScore = %v, want %v (placeholder)", saved.EditorialScore, editorial.NeutralScore)
	}
	if saved.ClassifierVersion != "placeholder-v0" {
		t.Errorf("ClassifierVersion = %q, want %q — disabled mode must reproduce exact pre-Phase-7 behavior", saved.ClassifierVersion, "placeholder-v0")
	}
}

func TestProcessCycle_ShadowModeNeverRejectsEvenWhenModelWouldReject(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Any article", OriginalURL: "https://example.com/e2",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	uc := newProcessCycleWithEditorial(fetcher, articles, decisions, &fakeClusterRepo{}, fakeClock{now: now}, alwaysRejectModel(), "shadow")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0 — shadow mode must never gate, even when the model would reject", result)
	}

	found := false
	for _, e := range decisions.events {
		if e.EventType == "editorial_shadow" {
			found = true
			if e.ClassifierVersion != "test-reject-v1" {
				t.Errorf("editorial_shadow event ClassifierVersion = %q, want %q", e.ClassifierVersion, "test-reject-v1")
			}
		}
	}
	if !found {
		t.Error("expected an editorial_shadow event to be recorded even though it didn't gate")
	}
}

// TestProcessCycle_EnforceModeRejectsInRejectZone also guards against the
// bug found in Phase 7's final whole-branch review: an enforce-mode reject
// routed through the generic reject() helper, which sets ReasonDetail but
// never Score/Threshold/ClassifierVersion — so editorial-review's report
// (which prints e.Score directly) showed score=0.0 for every enforced
// rejection even though the score is the entire point of auditing a reject
// decision. rejectEditorial (process_cycle.go) now carries all three on
// the same "editorial" event that also records the StatusRejectedQuality
// transition.
func TestProcessCycle_EnforceModeRejectsInRejectZone(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Any article", OriginalURL: "https://example.com/e3",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	model := alwaysRejectModel()
	uc := newProcessCycleWithEditorial(fetcher, articles, decisions, &fakeClusterRepo{}, fakeClock{now: now}, model, "enforce")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want rejected=1 accepted=0", result)
	}
	saved := articles.lastSaved()
	if saved.Status != article.StatusRejectedQuality {
		t.Errorf("Status = %v, want %v", saved.Status, article.StatusRejectedQuality)
	}

	wantScore := editorial.Predict(*model, editorial.FeatureVector{}).Value
	if wantScore == 0 {
		t.Fatalf("test fixture bug: wantScore computed as exactly 0, defeats the point of this assertion")
	}

	found := false
	for _, e := range decisions.events {
		if e.EventType == "editorial" && e.ToStatus == article.StatusRejectedQuality {
			found = true
			if e.Score != wantScore {
				t.Errorf("Score = %v, want %v (the editorial score that produced the reject decision)", e.Score, wantScore)
			}
			if e.Threshold != model.Thresholds.Accept {
				t.Errorf("Threshold = %v, want %v (model.Thresholds.Accept)", e.Threshold, model.Thresholds.Accept)
			}
			if e.ClassifierVersion != model.Version {
				t.Errorf("ClassifierVersion = %q, want %q", e.ClassifierVersion, model.Version)
			}
		}
	}
	if !found {
		t.Error("expected an \"editorial\" reject event recorded via rejectEditorial()")
	}
}

func TestProcessCycle_EnforceModeAcceptsAndFlagsAmbiguousZone(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Any article", OriginalURL: "https://example.com/e4",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	uc := newProcessCycleWithEditorial(fetcher, articles, decisions, &fakeClusterRepo{}, fakeClock{now: now}, alwaysAmbiguousModel(), "enforce")

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0 — the ambiguous zone flags, it doesn't block", result)
	}

	found := false
	for _, e := range decisions.events {
		if e.EventType == "editorial_ambiguous" {
			found = true
		}
	}
	if !found {
		t.Error("expected an editorial_ambiguous event to be recorded")
	}
}

func TestProcessCycle_EnforceModeSetsClassifierVersionAndQualityThreshold(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Any article", OriginalURL: "https://example.com/e5",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	model := alwaysAmbiguousModel()
	uc := newProcessCycleWithEditorial(fetcher, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, model, "enforce")

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	saved := articles.lastSaved()
	if saved.ClassifierVersion != model.Version {
		t.Errorf("ClassifierVersion = %q, want %q", saved.ClassifierVersion, model.Version)
	}
	if saved.QualityThreshold != model.Thresholds.Accept {
		t.Errorf("QualityThreshold = %v, want %v (the Accept threshold)", saved.QualityThreshold, model.Thresholds.Accept)
	}
}

func TestProcessCycle_AcceptsFreshArticleAndEnqueuesForPublication(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	repo := &fakeArticleRepo{}
	clusters := &fakeClusterRepo{}
	decisions := &fakeDecisionRepo{}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, decisions, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Fetched != 1 || result.Accepted != 1 || result.Rejected != 0 || result.Duplicate != 0 {
		t.Errorf("result = %+v, want fetched=1 accepted=1", result)
	}

	final := repo.lastSaved()
	if final.Status != article.StatusPendingPublication {
		t.Errorf("status = %v, want %v", final.Status, article.StatusPendingPublication)
	}
	if final.ID == "" {
		t.Error("expected generated ID, got empty")
	}
	if final.ClassifierVersion != "placeholder-v0" || final.DictionaryVersion != "placeholder-v0" {
		t.Errorf("ClassifierVersion/DictionaryVersion = %q/%q, want placeholder-v0/placeholder-v0", final.ClassifierVersion, final.DictionaryVersion)
	}
	if final.ClusterMemberRole != string(clustering.MemberRoleFirstReport) {
		t.Errorf("ClusterMemberRole = %q, want %q (a brand-new cluster's founding member)", final.ClusterMemberRole, clustering.MemberRoleFirstReport)
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != final.ID {
		t.Errorf("outbox.enqueued = %v, want [%q]", outbox.enqueued, final.ID)
	}
	if len(clusters.created) != 1 {
		t.Errorf("clusters.created = %v, want exactly 1 cluster", clusters.created)
	}
	// Two audit events: the clustering-specific one (event_type "clustering",
	// appended by ProcessCycle.cluster) and the final accept event.
	if len(decisions.events) != 2 {
		t.Fatalf("decisions.events = %+v, want 2 (clustering + accept)", decisions.events)
	}
	if decisions.events[0].EventType != "clustering" || decisions.events[0].ToStatus != article.StatusClustered {
		t.Errorf("decisions.events[0] = %+v, want event_type=clustering ending in Clustered", decisions.events[0])
	}
	if decisions.events[1].ToStatus != article.StatusPendingPublication {
		t.Errorf("decisions.events[1] = %+v, want an event ending in PendingPublication", decisions.events[1])
	}
}

func TestProcessCycle_RejectsStaleArticleWithoutEnqueuing(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/old-story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-200 * time.Hour),
	}}}
	repo := &fakeArticleRepo{}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Errorf("result = %+v, want rejected=1", result)
	}
	if len(outbox.enqueued) != 0 {
		t.Errorf("expected nothing enqueued for a stale article, got %v", outbox.enqueued)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusRejectedStale {
		t.Errorf("status = %v, want %v", final.Status, article.StatusRejectedStale)
	}
}

func TestProcessCycle_RejectsArticleFailingValidation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/no-date",
		ContentType: article.ContentTypeArticle,
		// PublishedAt intentionally left zero: article.Validate should
		// reject before dedup/clustering/freshness ever run.
	}}}
	repo := &fakeArticleRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 {
		t.Errorf("result = %+v, want rejected=1", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusRejectedValidation {
		t.Errorf("status = %v, want %v", final.Status, article.StatusRejectedValidation)
	}
}

func TestProcessCycle_SkipsExactDuplicate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	a := article.Article{
		OriginalURL: "https://example.com/dup-story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}
	fp := deduplication.Fingerprint(article.Normalize(a))
	fetcher := fakeFetcher{articles: []article.Article{a}}
	repo := &fakeArticleRepo{}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{knownFingerprint: fp}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Errorf("result = %+v, want duplicate=1", result)
	}
	if len(outbox.enqueued) != 0 {
		t.Error("expected nothing enqueued for a duplicate")
	}
	final := repo.lastSaved()
	if final.Status != article.StatusRejectedDuplicate {
		t.Errorf("status = %v, want %v", final.Status, article.StatusRejectedDuplicate)
	}
}

func TestProcessCycle_FetchErrorIsWrapped(t *testing.T) {
	fetchErr := errors.New("network down")
	fetcher := fakeFetcher{err: fetchErr}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: time.Now()})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, fetchErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, fetchErr)
	}
}

func TestProcessCycle_CreateClusterFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusterErr := errors.New("disk full")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{createErr: clusterErr}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, clusterErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, clusterErr)
	}
}

func TestProcessCycle_UnitOfWorkFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	uowErr := errors.New("commit failed")
	dedup := &fakeDeduplicationRepo{}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, dedup, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{err: uowErr}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, uowErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, uowErr)
	}
	// The fingerprint save happens inside the uow.Do closure now, so a
	// failed unit of work must leave no trace of it — otherwise the next
	// cycle would treat this never-persisted article as an exact
	// duplicate forever.
	if len(dedup.saved) != 0 {
		t.Errorf("dedup.saved = %v, want empty: fingerprint must not survive a failed unit of work", dedup.saved)
	}
}

func TestProcessCycle_DedupExistsFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	existsErr := errors.New("dedup lookup down")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{existsErr: existsErr}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, existsErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, existsErr)
	}
}

func TestProcessCycle_DedupSaveFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	saveErr := errors.New("fingerprint write failed")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{saveErr: saveErr}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, saveErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, saveErr)
	}
}

func TestProcessCycle_AcceptArticleSaveFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	saveErr := errors.New("article write failed")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{saveErr: saveErr}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, saveErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, saveErr)
	}
}

func TestProcessCycle_OutboxEnqueueFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	enqueueErr := errors.New("outbox write failed")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{enqueueErr: enqueueErr}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, enqueueErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, enqueueErr)
	}
}

func TestProcessCycle_RejectSaveFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/no-date",
		ContentType: article.ContentTypeArticle,
		// PublishedAt intentionally left zero to force the validation
		// (reject) path.
	}}}
	saveErr := errors.New("article write failed")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{saveErr: saveErr}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, saveErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, saveErr)
	}
}

func TestProcessCycle_RejectDecisionAppendFailureIsWrapped(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/no-date",
		ContentType: article.ContentTypeArticle,
		// PublishedAt intentionally left zero to force the validation
		// (reject) path.
	}}}
	appendErr := errors.New("decision log write failed")
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{appendErr: appendErr}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, appendErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, appendErr)
	}
}

func TestProcessCycle_ContextCancellationStopsProcessing(t *testing.T) {
	now := time.Now()
	fetcher := fakeFetcher{articles: []article.Article{
		{OriginalURL: "https://example.com/a", PublishedAt: now},
		{OriginalURL: "https://example.com/b", PublishedAt: now},
	}}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := uc.Run(ctx)
	if err == nil {
		t.Fatal("Run() error = nil, want context.Canceled")
	}
}

func TestProcessCycle_RejectsExactDuplicateViaExternalIDAlias(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		ExternalID: "ext-42", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/reposted", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	aliases := &fakeAliasRepository{byKey: map[string]string{aliasKey(out.AliasTypeExternalID, "rss:source-1:ext-42"): "original-article"}}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, aliases, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Errorf("result = %+v, want duplicate=1", result)
	}
	if len(outbox.enqueued) != 0 {
		t.Error("expected nothing enqueued for an alias-matched duplicate")
	}
}

func TestProcessCycle_NearDuplicateIsRejectedNotClustered(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/new-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Beat Sharks In An Overtime Thriller", ContentType: article.ContentTypeArticle},
	}}
	clusters := &fakeClusterRepo{}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, clusters, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("result = %+v, want duplicate=1 (near-duplicate titles)", result)
	}
	if len(clusters.created) != 0 || len(clusters.addedMember) != 0 {
		t.Error("a rejected near-duplicate must never touch clustering")
	}
	if len(outbox.enqueued) != 0 {
		t.Error("expected nothing enqueued for a near-duplicate")
	}
}

// --- Task 10: Gate A — policy-driven near-duplicate retention -------------

// TestProcessCycle_NearDuplicate_RetainedWhenPolicyAllows mirrors
// TestProcessCycle_NearDuplicateIsRejectedNotClustered's near-duplicate
// setup (same-shaped title pair via fakeSimilarityIndexRepository), but the
// incoming article's source has a policy with RetainNearDuplicates=true and
// the resolver is in sourcepolicy.ModeEnforce — Gate A must let it fall
// through to clustering instead of auto-rejecting it as a plain duplicate.
func TestProcessCycle_NearDuplicate_RetainedWhenPolicyAllows(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Beat Sharks In An Overtime Thriller", ContentType: article.ContentTypeArticle},
	}}
	clusters := &fakeClusterRepo{}
	repo := &fakeArticleRepo{}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: true},
		},
	}
	uc := newProcessCycleWithSourcePolicy(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, resolver)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 0 {
		t.Errorf("result = %+v, want duplicate=0: a policy-allowed near-duplicate must not auto-reject", result)
	}
	final := repo.lastSaved()
	if final.Status == article.StatusRejectedDuplicate {
		t.Fatal("article with RetainNearDuplicates policy was rejected as a plain duplicate")
	}
	if len(clusters.created) == 0 && len(clusters.addedMember) == 0 {
		t.Error("expected the retained near-duplicate to proceed into clustering, not stop at the dedup gate")
	}
}

// TestProcessCycle_NearDuplicate_RejectedWhenNoPolicy is the same
// near-duplicate scenario, but the resolver (running in ModeEnforce) has no
// policy entry for this particular source, so Resolve falls back to
// sourcepolicy.DefaultPolicy (RetainNearDuplicates=false) — behavior must
// be byte-identical to pre-Phase-12: rejected, exactly like
// TestProcessCycle_NearDuplicateIsRejectedNotClustered. This proves Gate A
// is a no-op for every source without an explicit policy, even when the
// resolver itself is actively enforcing for other sources.
func TestProcessCycle_NearDuplicate_RejectedWhenNoPolicy(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: source.Source{ID: mustID(t, source.TypeRSS, "another-regular-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/new-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Beat Sharks In An Overtime Thriller", ContentType: article.ContentTypeArticle},
	}}
	clusters := &fakeClusterRepo{}
	repo := &fakeArticleRepo{}
	resolver := &fakeSourcePolicyResolver{mode: sourcepolicy.ModeEnforce}
	uc := newProcessCycleWithSourcePolicy(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, resolver)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("result = %+v, want duplicate=1 (unconfigured source must reject exactly as before)", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusRejectedDuplicate {
		t.Errorf("status = %v, want %v", final.Status, article.StatusRejectedDuplicate)
	}
	if len(clusters.created) != 0 || len(clusters.addedMember) != 0 {
		t.Error("a rejected near-duplicate must never touch clustering")
	}
}

// TestProcessCycle_NearDuplicate_ShadowModeRecordsWouldRetainDecision is the
// final-whole-branch-review fix for Finding 1: shadow mode must reject a
// near-duplicate exactly as it always has (shadow mode never changes actual
// behavior — see TestProcessCycle_NearDuplicate_RejectedWhenNoPolicy for the
// no-policy case this must not disturb), but when the resolver is in
// sourcepolicy.ModeShadow and the source's policy has RetainNearDuplicates
// true, Gate A must now also persist a source_policy_decisions row recording
// that enforce mode would have retained it — this is the "what if" audit
// trail the shadow->enforce promotion runbook
// (docs/OPERATIONS.md) depends on.
func TestProcessCycle_NearDuplicate_ShadowModeRecordsWouldRetainDecision(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Beat Sharks In An Overtime Thriller", ContentType: article.ContentTypeArticle},
	}}
	clusters := &fakeClusterRepo{}
	repo := &fakeArticleRepo{}
	sourcePolicies := &fakeSourcePolicyRepository{}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{
			"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: true},
		},
	}
	uc := newProcessCycleWithSourcePolicyRepo(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, resolver, sourcePolicies)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("result = %+v, want duplicate=1: shadow mode must never change actual reject behavior", result)
	}
	final := repo.lastSaved()
	if final.Status != article.StatusRejectedDuplicate {
		t.Errorf("Status = %v, want %v: shadow mode must reject exactly as before", final.Status, article.StatusRejectedDuplicate)
	}
	if len(clusters.created) != 0 || len(clusters.addedMember) != 0 {
		t.Error("shadow mode must never let a near-duplicate reach clustering")
	}

	records, err := sourcePolicies.ByArticle(context.Background(), final.ID)
	if err != nil {
		t.Fatalf("ByArticle() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want exactly one shadow-mode preview record", records)
	}
	rec := records[0]
	if rec.DecisionCode != string(sourcepolicy.CodeWouldRetainNearDuplicate) {
		t.Errorf("DecisionCode = %q, want %q", rec.DecisionCode, sourcepolicy.CodeWouldRetainNearDuplicate)
	}
	if rec.Mode != string(sourcepolicy.ModeShadow) {
		t.Errorf("Mode = %q, want %q", rec.Mode, sourcepolicy.ModeShadow)
	}
	if rec.WouldPublishStandard {
		t.Error("WouldPublishStandard = true, want false: standard behavior rejects this article, it never publishes")
	}
	if rec.WouldPublishSpecial {
		t.Error("WouldPublishSpecial = true, want false: the eventual publish outcome is unknown since clustering never ran for this preview")
	}
	if rec.ClusterID != "" {
		t.Errorf("ClusterID = %q, want \"\": no cluster exists yet at Gate A", rec.ClusterID)
	}
	if rec.MemberRole != "" || rec.Novelty != "" {
		t.Errorf("MemberRole = %q, Novelty = %q, want both empty: genuinely unknown at Gate A", rec.MemberRole, rec.Novelty)
	}
	if rec.DedupMatchType != "near" {
		t.Errorf("DedupMatchType = %q, want %q", rec.DedupMatchType, "near")
	}
}

// TestProcessCycle_NearDuplicate_ShadowModeNoPolicyRecordsNothing proves
// Gate A's shadow-mode audit recording is scoped exactly as documented: a
// source with no configured policy (RetainNearDuplicates false, the
// DefaultPolicy) has nothing policy-relevant to report even while the
// resolver itself is actively in ModeShadow, so no audit row is written —
// matching Gate B's existing "only record when something non-default
// happened" convention.
func TestProcessCycle_NearDuplicate_ShadowModeNoPolicyRecordsNothing(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: source.Source{ID: mustID(t, source.TypeRSS, "another-regular-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/new-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Beat Sharks In An Overtime Thriller", ContentType: article.ContentTypeArticle},
	}}
	clusters := &fakeClusterRepo{}
	repo := &fakeArticleRepo{}
	sourcePolicies := &fakeSourcePolicyRepository{}
	resolver := &fakeSourcePolicyResolver{mode: sourcepolicy.ModeShadow}
	uc := newProcessCycleWithSourcePolicyRepo(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, resolver, sourcePolicies)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("result = %+v, want duplicate=1", result)
	}
	final := repo.lastSaved()
	records, err := sourcePolicies.ByArticle(context.Background(), final.ID)
	if err != nil {
		t.Fatalf("ByArticle() error = %v", err)
	}
	if len(records) != 0 {
		t.Errorf("records = %+v, want none: no policy is configured for this source", records)
	}
}

// --- Task 11: Gate B — publication decision and audit trail ---------------
//
// These reuse Task 8's fakeClusterRepo-with-preset-candidates pattern (see
// that section's comment) to drive clustering.DecideJoin to a specific
// MemberRole deterministically, then layer a source policy + enforce mode
// on top to exercise Gate B's ShouldPublish branch.

// TestProcessCycle_OfficialConfirmation_NoNewInfo_RetainedNotPublished
// mirrors TestProcessCycle_ClusterJoinsExistingClusterOnMatchingEventSignals'
// fixture (same entity/action, different source => MemberRoleConfirmation),
// but attaches a policy that allows official-confirmation handling while
// disabling publish-without-new-information. Gate B must retain the article
// (StatusRetainedNoPublish) instead of publishing it, and record exactly one
// audit row with CodeOfficialConfirmationNoPublish.
func TestProcessCycle_OfficialConfirmation_NoNewInfo_RetainedNotPublished(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/sprisotopes-jane-doe-confirm", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	outbox := &fakeOutboxRepo{}
	repo := &fakeArticleRepo{}
	sourcePolicies := &fakeSourcePolicyRepository{}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			"rss:sprisotopes": {
				SourceID: "rss:sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam,
				RetainNearDuplicates: true, AllowOfficialConfirmation: true,
				PublishConfirmationWithoutNewInformation: false,
			},
		},
	}
	uc := newProcessCycleWithSourcePolicyRepo(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now}, resolver, sourcePolicies)

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	final := repo.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q (Gate B's CodeOfficialConfirmationNoPublish path requires this role)", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if final.Status != article.StatusRetainedNoPublish {
		t.Errorf("Status = %q, want %q", final.Status, article.StatusRetainedNoPublish)
	}
	if len(outbox.enqueued) != 0 {
		t.Error("article was enqueued to outbox despite policy saying no-publish")
	}

	records, err := sourcePolicies.ByArticle(context.Background(), final.ID)
	if err != nil {
		t.Fatalf("ByArticle() error = %v", err)
	}
	if len(records) != 1 || records[0].DecisionCode != string(sourcepolicy.CodeOfficialConfirmationNoPublish) {
		t.Errorf("records = %+v, want exactly one CodeOfficialConfirmationNoPublish record", records)
	}
}

// TestProcessCycle_SubstantialUpdate_PublishedWhenAllowed mirrors
// TestProcessCycle_ClusterSubstantialUpdateJoinsAndSwitchesRepresentative's
// fixture (same entity, differing action, plus a named opponent the
// candidate lacks => MemberRoleSubstantialUpdate) with a policy that allows
// substantial-update publication. Gate B must let the article through to
// Step 7/8 and it must end up published, exactly as it would pre-Phase-12.
func TestProcessCycle_SubstantialUpdate_PublishedWhenAllowed(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Release Jane Doe Ahead Of Matchup With Sharks", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/sprisotopes-jane-doe-release-update", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	outbox := &fakeOutboxRepo{}
	repo := &fakeArticleRepo{}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			"rss:sprisotopes": {SourceID: "rss:sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam, AllowSubstantialUpdate: true, MinimumUpdateNovelty: 0.1},
		},
	}
	uc := newProcessCycleWithSourcePolicy(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now}, resolver)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0: a policy-allowed substantial update must still publish", result)
	}

	final := repo.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleSubstantialUpdate) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q (Gate B's CodePublishSubstantialUpdate path requires this role)", final.ClusterMemberRole, clustering.MemberRoleSubstantialUpdate)
	}
	if final.Status != article.StatusPendingPublication {
		t.Errorf("Status = %q, want %q", final.Status, article.StatusPendingPublication)
	}
	if len(outbox.enqueued) != 1 {
		t.Error("substantial update was not enqueued to outbox")
	}
}

// TestProcessCycle_LowQualityOfficialSource_StillRejectedByEditorial is the
// issue's explicit test case: being "official" — and eligible for a
// substantial-update publish per source policy (ShouldPublish=true from
// Gate B) — never bypasses editorial quality. Combines
// TestProcessCycle_EnforceModeRejectsInRejectZone's editorial fixture
// (alwaysRejectModel + Editorial.Mode "enforce" rejects every article
// regardless of content, into ZoneReject) with this file's own
// substantial-update clustering fixture (see
// TestProcessCycle_SubstantialUpdate_PublishedWhenAllowed above): Gate B
// runs first (Step 6->7 in processOne) and says ShouldPublish=true, so it
// does not short-circuit — but Step 7's editorial gate, running
// immediately after, must still reject it.
func TestProcessCycle_LowQualityOfficialSource_StillRejectedByEditorial(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Release Jane Doe Ahead Of Matchup With Sharks", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/sprisotopes-jane-doe-release-update-lowq", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	articles := &fakeArticleRepo{}
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			"rss:sprisotopes": {SourceID: "rss:sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam, AllowSubstantialUpdate: true, MinimumUpdateNovelty: 0.1},
		},
	}
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: "enforce", AmbiguousPolicy: "reject"}
	model := alwaysRejectModel()
	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, decisions, outbox, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, model, nil, cascade, fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleSubstantialUpdate) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q (so Gate B's ShouldPublish=true is actually exercised, not skipped by an earlier gate)", final.ClusterMemberRole, clustering.MemberRoleSubstantialUpdate)
	}
	if final.Status != article.StatusRejectedQuality {
		t.Errorf("Status = %q, want %q: an official, policy-permitted publish must still be blocked by editorial quality", final.Status, article.StatusRejectedQuality)
	}
	if len(outbox.enqueued) != 0 {
		t.Error("article was enqueued to outbox despite failing editorial quality")
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Errorf("result = %+v, want accepted=0 rejected=1", result)
	}

	found := false
	for _, e := range decisions.events {
		if e.EventType == "editorial" && e.ToStatus == article.StatusRejectedQuality {
			found = true
		}
	}
	if !found {
		t.Error("expected an \"editorial\" reject event recorded via rejectEditorial(), proving Step 7 (not Gate B) made the final call")
	}
}

// --- Task 8: event-clustering integration scenarios -----------------------
//
// These use clusterTestSettings() (a small but real eventsignals/clustering
// configuration) together with a fakeClusterRepo whose candidates field
// directly supplies the existing-cluster member(s) FindCandidatesByEventKeys
// would otherwise have found via the (real, SQL-backed) event-key index —
// see fakeClusterRepo.FindCandidatesByEventKeys' doc comment. The incoming
// article's own eventsignals.Representation is still derived for real, by
// ProcessCycle itself, from the fetched article's title via
// clusterTestSettings().Rules.

func TestProcessCycle_ClusterJoinsExistingClusterOnMatchingEventSignals(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-signing", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, decisions, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.created) != 0 {
		t.Errorf("clusters.created = %v, want none: a matching event should join, not create", clusters.created)
	}
	if len(clusters.addedMember) != 1 {
		t.Fatalf("clusters.addedMember = %+v, want exactly 1 join", clusters.addedMember)
	}
	member := clusters.addedMember[0]
	if member.clusterID != "cluster-existing-1" {
		t.Errorf("joined clusterID = %q, want cluster-existing-1", member.clusterID)
	}
	if member.role != clustering.MemberRoleConfirmation {
		t.Errorf("member.role = %v, want %v (same action, second source)", member.role, clustering.MemberRoleConfirmation)
	}
	if len(clusters.decisions) != 1 || clusters.decisions[0].decision.Action != clustering.ActionJoin {
		t.Fatalf("clusters.decisions = %+v, want 1 ActionJoin decision", clusters.decisions)
	}
	foundEntityEvidence := false
	for _, e := range clusters.decisions[0].decision.Evidence {
		if strings.Contains(string(e), "Jane Doe") {
			foundEntityEvidence = true
		}
	}
	if !foundEntityEvidence {
		t.Errorf("decision evidence = %v, want an entity-match line naming Jane Doe", clusters.decisions[0].decision.Evidence)
	}
	if len(decisions.events) != 2 || decisions.events[0].EventType != "clustering" {
		t.Fatalf("decisions.events = %+v, want [clustering, accept]", decisions.events)
	}
}

func TestProcessCycle_ClusterCreatesNewClusterWhenNoCandidateMatches(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign John Smith", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/john-smith-signing", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{} // no candidates configured
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.created) != 1 {
		t.Fatalf("clusters.created = %v, want exactly 1 new cluster", clusters.created)
	}
	if len(clusters.addedMember) != 0 {
		t.Errorf("clusters.addedMember = %+v, want none: no candidate should join", clusters.addedMember)
	}
	if len(clusters.decisions) != 1 || clusters.decisions[0].decision.Action != clustering.ActionCreate {
		t.Fatalf("clusters.decisions = %+v, want 1 ActionCreate decision", clusters.decisions)
	}
}

func TestProcessCycle_ClusterSavesUnknownEntityOccurrences(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Random Newcomer", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/random-newcomer-signing", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	unknownEntities := &fakeUnknownEntityRepository{}
	uc := usecase.NewProcessCycle(
		fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil,
		defaultCascade(), fakeClock{now: now}, fakeLogger{}, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{},
		&fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, unknownEntities,
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if unknownEntities.calls != 1 {
		t.Errorf("SaveOccurrences calls = %d, want 1 (title contains \"Sign Random Newcomer\" once \"Isotopes\" is split off as a known team — an uncataloged run of otherwise-unmatched Title-Case words)", unknownEntities.calls)
	}
}

func TestProcessCycle_ClusterUnknownEntitySaveErrorDoesNotFailProcessing(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Random Newcomer", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/random-newcomer-signing", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	unknownEntities := &fakeUnknownEntityRepository{err: errors.New("boom")}
	uc := usecase.NewProcessCycle(
		fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil,
		defaultCascade(), fakeClock{now: now}, fakeLogger{}, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{},
		&fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, unknownEntities,
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v, want nil — an unknown-entity save failure must never fail article processing", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1 despite the SaveOccurrences error", result)
	}
	if unknownEntities.calls != 1 {
		t.Errorf("SaveOccurrences calls = %d, want 1", unknownEntities.calls)
	}
}

// fakeEntityResolutionLLM is a hand-written test double for
// out.EntityResolutionLLM (no mocking framework in this codebase) that
// always resolves any candidate to a fixed player.
type fakeEntityResolutionLLM struct {
	result out.EntityResolutionResult
	err    error
}

func (f *fakeEntityResolutionLLM) Resolve(ctx context.Context, c out.EntityResolutionCandidate) (out.EntityResolutionResult, error) {
	return f.result, f.err
}

// fakeEntityProposalRepository is a hand-written test double for
// entityproposalport.EntityProposalRepository (no mocking framework in
// this codebase) whose every method is a no-op returning zero
// values/nil — this test only cares about the LLM resolution path, not
// caching persistence, which Task 5's own tests already cover.
type fakeEntityProposalRepository struct{}

func (f *fakeEntityProposalRepository) Upsert(ctx context.Context, p entityproposal.Proposal) (string, error) {
	return "", nil
}

func (f *fakeEntityProposalRepository) Get(ctx context.Context, id string) (entityproposal.Proposal, error) {
	return entityproposal.Proposal{}, nil
}

func (f *fakeEntityProposalRepository) FindByNormalizedName(ctx context.Context, normalizedName string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	return entityproposal.Proposal{}, false, nil
}

func (f *fakeEntityProposalRepository) FindByAlias(ctx context.Context, normalizedAlias string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	return entityproposal.Proposal{}, false, nil
}

func (f *fakeEntityProposalRepository) List(ctx context.Context, entityType, status string) ([]entityproposal.Proposal, error) {
	return nil, nil
}

func (f *fakeEntityProposalRepository) UpdateStatus(ctx context.Context, id string, status entityproposal.Status, decidedAt time.Time) error {
	return nil
}

func (f *fakeEntityProposalRepository) FilterUnprocessed(ctx context.Context, extractorVersion string, articleIDs []string) ([]string, error) {
	return nil, nil
}

func (f *fakeEntityProposalRepository) MarkArticlesProcessed(ctx context.Context, extractorVersion string, articleIDs []string, processedAt time.Time) error {
	return nil
}

// TestProcessCycle_Cluster_EntityFallbackResolvesUnknownEntityIntoPrimaryEntity
// proves that, once the deterministic extractor reports an
// eventsignals.UnknownEntity for "Random Newcomer" (the same fixture
// TestProcessCycle_ClusterSavesUnknownEntityOccurrences uses — "Isotopes"
// splits off as the known team, "Sign Random Newcomer" is left
// unmatched), an entityFallback that resolves "Random Newcomer" to a
// player changes the article's PrimaryEntity for a second, augmented
// eventsignals.Extract pass. Without entityFallback, PrimaryEntity falls
// back to the known team ("Springfield Isotopes") per eventsignals.Extract's
// own PrimaryEntity selection order (player, then team, then
// competition) — it is not "" for this fixture, since "Isotopes" is
// itself a cataloged team entity.
func TestProcessCycle_Cluster_EntityFallbackResolvesUnknownEntityIntoPrimaryEntity(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	newFetcher := func() fakeFetcher {
		return fakeFetcher{articles: []article.Article{{
			Title: "Isotopes Sign Random Newcomer", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
			OriginalURL: "https://example.com/random-newcomer-signing", ContentType: article.ContentTypeArticle,
			PublishedAt: now.Add(-1 * time.Hour),
		}}}
	}

	// --- With entityFallback set: "Random Newcomer" resolves to a
	// player, so the second Extract pass reports it as PrimaryEntity.
	articlesWithFallback := &fakeArticleRepo{}
	fallback := &entityresolution.Deps{
		LLM: &fakeEntityResolutionLLM{result: out.EntityResolutionResult{
			Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9,
		}},
		Cache:         &fakeEntityProposalRepository{},
		MinConfidence: 0.5,
		Logger:        fakeLogger{},
	}
	unknownEntitiesWithFallback := &fakeUnknownEntityRepository{}
	logger := newRecordingLogger()
	ucWithFallback := usecase.NewProcessCycle(
		newFetcher(), articlesWithFallback, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil,
		defaultCascade(), fakeClock{now: now}, logger, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{},
		&fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, unknownEntitiesWithFallback,
		fallback,
	)
	if _, err := ucWithFallback.Run(context.Background()); err != nil {
		t.Fatalf("Run() [with fallback] error = %v", err)
	}
	gotWithFallback := articlesWithFallback.lastSaved().PrimaryEntity
	if gotWithFallback != "Random Newcomer" {
		t.Errorf("PrimaryEntity (with entityFallback) = %q, want %q", gotWithFallback, "Random Newcomer")
	}
	// Finding 5 of the 2026-09-16 final review: a successful resolution
	// that changes the article's representation must leave an audit
	// trail an operator can notice (and correct) after the fact.
	foundAuditLog := false
	for _, c := range *logger.infoCalls {
		if c.msg == "entity resolution fallback changed representation" {
			foundAuditLog = true
			break
		}
	}
	if !foundAuditLog {
		t.Errorf("logger.infoCalls = %+v, want an \"entity resolution fallback changed representation\" Info call", *logger.infoCalls)
	}
	// This is the riskiest invariant this task introduces: SaveOccurrences
	// must still be called with the FIRST pass's (raw, pre-resolution)
	// UnknownEntities — cluster()'s rawUnknownEntities capture — not the
	// second, augmented pass's result, which for a successfully resolved
	// candidate no longer reports "Random Newcomer" as unknown at all (the
	// augmented rules now "know" about it). A regression that reverted
	// cluster() to persist rep.UnknownEntities instead of
	// rawUnknownEntities would make this call vanish (calls == 0) without
	// TestProcessCycle_ClusterSavesUnknownEntityOccurrences (entityFallback:
	// nil, where the two lists are identical by construction) ever noticing.
	if unknownEntitiesWithFallback.calls != 1 {
		t.Fatalf("SaveOccurrences calls = %d, want 1 (raw pre-resolution evidence must still be recorded)", unknownEntitiesWithFallback.calls)
	}
	// The raw first-pass candidate text is "Sign Random Newcomer" (the
	// whole unmatched Title-Case run once "Isotopes" splits off as the
	// known team — see TestProcessCycle_ClusterSavesUnknownEntityOccurrences's
	// own comment for why); the fallback resolves it to the canonical
	// player name "Random Newcomer", but the RAW evidence persisted here
	// must still be the original, unresolved span.
	foundRawCandidate := false
	for _, occ := range unknownEntitiesWithFallback.lastOccurrences {
		if occ.Text == "Sign Random Newcomer" {
			foundRawCandidate = true
			break
		}
	}
	if !foundRawCandidate {
		t.Errorf("SaveOccurrences occurrences = %+v, want it to still include the raw candidate %q (unaffected by the fallback resolving it)", unknownEntitiesWithFallback.lastOccurrences, "Sign Random Newcomer")
	}

	// --- Control: entityFallback nil, same fixture — PrimaryEntity stays
	// the known team, proving the fallback (not some other change) is
	// what moved PrimaryEntity above.
	articlesNoFallback := &fakeArticleRepo{}
	ucNoFallback := usecase.NewProcessCycle(
		newFetcher(), articlesNoFallback, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil,
		defaultCascade(), fakeClock{now: now}, fakeLogger{}, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{},
		&fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)
	if _, err := ucNoFallback.Run(context.Background()); err != nil {
		t.Fatalf("Run() [no fallback] error = %v", err)
	}
	gotNoFallback := articlesNoFallback.lastSaved().PrimaryEntity
	if gotNoFallback != "Springfield Isotopes" {
		t.Errorf("PrimaryEntity (no entityFallback) = %q, want %q", gotNoFallback, "Springfield Isotopes")
	}

	if gotWithFallback == gotNoFallback {
		t.Fatalf("PrimaryEntity was %q in both runs — entityFallback had no observable effect", gotWithFallback)
	}
}

func TestProcessCycle_ClusterAmbiguousDecisionRetainedWithoutPublishing(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		// Same entity ("Jane Doe") and same rival source as the candidate,
		// but a different action ("release" vs "sign") — this lands the
		// Compare score at ~0.647 (see clusterTestSettings' weights), inside
		// [ambiguous_high, join_threshold) => ActionAmbiguous.
		Title: "Isotopes Release Jane Doe", Source: source.Source{ID: mustID(t, source.TypeRSS, "rival-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-release", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rss:rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Fatalf("result = %+v, want accepted=0 rejected=1: an ambiguous clustering decision should be retained before publication", result)
	}
	if len(outbox.enqueued) != 0 {
		t.Errorf("outbox.enqueued = %v, want none: ambiguous clustering must not reach publication", outbox.enqueued)
	}
	if len(clusters.decisions) != 1 {
		t.Fatalf("clusters.decisions = %+v, want 1 saved decision", clusters.decisions)
	}
	if clusters.decisions[0].decision.Action != clustering.ActionAmbiguous {
		t.Errorf("decision.Action = %v, want %v", clusters.decisions[0].decision.Action, clustering.ActionAmbiguous)
	}
	// Still create a new open cluster to preserve the one-cluster-per-article
	// invariant, and never join the candidate cluster it was ambiguous
	// against.
	if len(clusters.created) != 1 {
		t.Fatalf("clusters.created = %v, want exactly 1 fallback cluster", clusters.created)
	}
	if len(clusters.addedMember) != 0 {
		t.Errorf("clusters.addedMember = %+v, want none: ambiguous must never join", clusters.addedMember)
	}
	if clusters.decisions[0].chosenClusterID != clusters.created[0] {
		t.Errorf("chosenClusterID = %q, want the fallback cluster %q", clusters.decisions[0].chosenClusterID, clusters.created[0])
	}
}

func TestProcessCycle_ClusterSubstantialUpdateJoinsAndSwitchesRepresentative(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		// Same entity, differing action (progression: "sign" -> "release"),
		// and a named opponent the candidate lacks — enough novelty+
		// completeness for clustering.ReplacesCurrent to switch the
		// representative once joined (see clusterTestSettings'
		// RepresentativeConfig: Completeness/NoveltyWeight only).
		Title: "Isotopes Release Jane Doe Ahead Of Matchup With Sharks", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-release-update", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	outbox := &fakeOutboxRepo{}
	repo := &fakeArticleRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1: a substantial update must remain publishable", result)
	}
	final := repo.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleSubstantialUpdate) {
		t.Errorf("ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleSubstantialUpdate)
	}
	if final.PrimaryEntity != "Jane Doe" {
		t.Errorf("PrimaryEntity = %q, want %q", final.PrimaryEntity, "Jane Doe")
	}
	if len(outbox.enqueued) != 1 {
		t.Errorf("outbox.enqueued = %v, want 1", outbox.enqueued)
	}
	if len(clusters.addedMember) != 1 {
		t.Fatalf("clusters.addedMember = %+v, want exactly 1 join", clusters.addedMember)
	}
	if clusters.addedMember[0].role != clustering.MemberRoleSubstantialUpdate {
		t.Errorf("member.role = %v, want %v", clusters.addedMember[0].role, clustering.MemberRoleSubstantialUpdate)
	}
	if len(clusters.representativeUpdates) != 1 {
		t.Fatalf("clusters.representativeUpdates = %+v, want exactly 1 representative switch", clusters.representativeUpdates)
	}
	if clusters.representativeUpdates[0].clusterID != "cluster-existing-1" {
		t.Errorf("representative switch clusterID = %q, want cluster-existing-1", clusters.representativeUpdates[0].clusterID)
	}
}

// TestProcessCycle_RepresentativePreference_ComesFromSourcePolicy_NotClassificationList
// is the Task 12 conflation fix's regression test: maybeUpdateRepresentative
// must derive clustering.RepresentativeCandidate.IsPrimarySource from the
// resolved source policy's RepresentativePreference, not from
// Classification.TrustedSourceIDs (the unrelated relevance-bypass list —
// left completely untouched by this task).
//
// Fixture shape mirrors TestProcessCycle_ClusterSubstantialUpdateJoinsAndSwitchesRepresentative
// above (a preset fakeClusterRepo candidate standing in for the cluster's
// current representative — see winningClusterCandidate's doc comment — and
// one fetched article that joins that same cluster), but with the incoming
// and existing Representations engineered to be near-identical in every
// novelty/completeness signal (same entity/action/event type, no opponent
// on either side): both clear ReplacesCurrent's SubstantialUpdateThreshold
// gate on completeness alone (independent of IsPrimarySource), but neither
// clears RepresentativeSwitchMargin on novelty+completeness alone. Only
// "sprisotopes" being configured with RepresentativePreference: PreferenceHigh
// — and deliberately absent from Classification.TrustedSourceIDs, which is
// left nil/empty here — supplies enough extra weighted score (via a
// PrimarySourceWeight-heavy RepresentativeConfig) to clear that margin.
// Before the fix this never switches (IsPrimarySource is false on both
// sides, since TrustedSourceIDs never mentions either source); after the
// fix it switches to the incoming article.
func TestProcessCycle_RepresentativePreference_ComesFromSourcePolicy_NotClassificationList(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		// Same entity/action/event type as the existing candidate below —
		// engineered to add zero novelty and identical completeness, so any
		// representative switch can only be explained by the primary-source
		// signal, not by content richness.
		Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/sprisotopes-jane-doe-signing", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "regular-beat-writer", Domain: "regular-beat-writer.example",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	repo := &fakeArticleRepo{}

	cascade := defaultCascade()
	// A PrimarySourceWeight-heavy RepresentativeConfig: CompletenessWeight/
	// NoveltyWeight are still present (so the two candidates' near-identical
	// content still clears SubstantialUpdateThreshold on completeness alone,
	// exactly like clusterTestSettings' Representative config), but
	// PrimarySourceWeight (5, versus 1 each for the other two) dominates the
	// weighted average — large enough that only "sprisotopes" being
	// IsPrimarySource can push candidateScore-currentScore past
	// RepresentativeSwitchMargin (0.2); by hand: with neither side primary,
	// both candidates score 0.6/7 ≈ 0.086 (a zero-margin tie, no switch);
	// with only the incoming side primary, it scores 5.6/7 = 0.8, a 0.714
	// margin over the unchanged current score — comfortably past 0.2.
	cascade.Cluster.Representative = clustering.RepresentativeConfig{
		CompletenessWeight:         1,
		NoveltyWeight:              1,
		PrimarySourceWeight:        5,
		SubstantialUpdateThreshold: 0.05,
		RepresentativeSwitchMargin: 0.2,
		DefaultDomainPriority:      50,
		RulesVersion:               "test-cluster-rules-v1",
	}

	// "sprisotopes" is NOT in Classification.TrustedSourceIDs (that map is
	// left nil by defaultCascade — the relevance-bypass list this task must
	// not touch) but IS configured with a high representative preference:
	// exactly the split the conflation fix requires. Mode is left at its
	// zero value (ModeDisabled): unlike Gate A/B, neither call site this
	// task touches gates on sourcePolicyResolver.Mode(), so representative
	// preference must take effect regardless of enforce/shadow/disabled.
	resolver := &fakeSourcePolicyResolver{
		policies: map[string]sourcepolicy.Policy{
			"rss:sprisotopes": {SourceID: "rss:sprisotopes", RepresentativePreference: sourcepolicy.PreferenceHigh},
		},
	}

	uc := usecase.NewProcessCycle(
		fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil,
		cascade, fakeClock{now: now}, fakeLogger{}, &fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	final := repo.lastSaved()
	if len(clusters.representativeUpdates) != 1 {
		t.Fatalf("clusters.representativeUpdates = %+v, want exactly 1 representative switch (driven by sprisotopes' source-policy representative preference)", clusters.representativeUpdates)
	}
	update := clusters.representativeUpdates[0]
	if update.clusterID != "cluster-existing-1" {
		t.Errorf("representative switch clusterID = %q, want cluster-existing-1", update.clusterID)
	}
	if update.articleID != final.ID {
		t.Errorf("representative switch articleID = %q, want %q (the newly-joined sprisotopes article)", update.articleID, final.ID)
	}
}

func TestProcessCycle_ClusterSameEntityDifferentActionCreatesSeparateCluster(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		// Same entity ("Jane Doe") as the candidate, but an unrelated event
		// (an injury report vs. a signing) — incompatible EventType/Action
		// must hard-disqualify the candidate (clustering.detectConflict),
		// never joining just because the subject happens to match.
		Title: "Jane Doe Suffers Injury In Practice", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-injury", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.addedMember) != 0 {
		t.Errorf("clusters.addedMember = %+v, want none: a different event about the same entity must not join", clusters.addedMember)
	}
	if len(clusters.created) != 1 {
		t.Fatalf("clusters.created = %v, want exactly 1 new (separate) cluster", clusters.created)
	}
	if len(clusters.decisions) != 1 || clusters.decisions[0].decision.Action != clustering.ActionCreate {
		t.Fatalf("clusters.decisions = %+v, want 1 ActionCreate decision", clusters.decisions)
	}
}

func TestProcessCycle_ClusterRumorFollowedByConfirmationJoinsWithConfirmationRole(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Jane Doe's Move To Isotopes Is Confirmed", Source: source.Source{ID: mustID(t, source.TypeRSS, "team-site.com"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-confirmed", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-rumor", ClusterID: "cluster-rumor",
		Representation: eventsignals.Representation{
			ArticleID: "existing-rumor", SourceID: "rumor-source", Domain: "rumor-site.com",
			PrimaryEntity: "Jane Doe", EventType: eventsignals.EventTypeRumor, ContentSignal: eventsignals.ContentSignalRumor,
			EventDate: now.Add(-3 * time.Hour), EventWindow: 24 * time.Hour,
		},
	}}}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.addedMember) != 1 {
		t.Fatalf("clusters.addedMember = %+v, want exactly 1 join", clusters.addedMember)
	}
	member := clusters.addedMember[0]
	if member.clusterID != "cluster-rumor" {
		t.Errorf("joined clusterID = %q, want cluster-rumor", member.clusterID)
	}
	if member.role != clustering.MemberRoleConfirmation {
		t.Errorf("member.role = %v, want %v", member.role, clustering.MemberRoleConfirmation)
	}
	if len(clusters.decisions) != 1 || clusters.decisions[0].decision.Action != clustering.ActionJoin {
		t.Fatalf("clusters.decisions = %+v, want 1 ActionJoin decision", clusters.decisions)
	}
	foundEntityEvidence := false
	for _, e := range clusters.decisions[0].decision.Evidence {
		if strings.Contains(string(e), "Jane Doe") {
			foundEntityEvidence = true
		}
	}
	if !foundEntityEvidence {
		t.Errorf("decision evidence = %v, want an entity-match line naming Jane Doe", clusters.decisions[0].decision.Evidence)
	}
	// The confirmation names an action (confirm) and a more specific
	// EventType the rumor's bare Representation lacks, clearing
	// clusterTestSettings' Representative config thresholds: representative
	// selection should switch away from the rumor to this more complete,
	// more novel confirmation.
	if len(clusters.representativeUpdates) != 1 {
		t.Fatalf("clusters.representativeUpdates = %+v, want exactly 1 representative switch (the confirmation is more complete/novel than the bare rumor)", clusters.representativeUpdates)
	}
	if clusters.representativeUpdates[0].clusterID != "cluster-rumor" {
		t.Errorf("representative switch clusterID = %q, want cluster-rumor", clusters.representativeUpdates[0].clusterID)
	}
}

// Scenario: a transaction already reported by two sources (an official
// announcement plus one confirmation, already in the same cluster) gets a
// third, independent confirmation. It must join the existing cluster, not
// spawn a third cluster, with MemberRoleConfirmation and evidence naming the
// matched entity — the multi-source mechanics the two-source
// TestProcessCycle_ClusterJoinsExistingClusterOnMatchingEventSignals test
// above does not exercise (only one existing candidate there).
//
// See representative_test.go's
// TestRankRepresentative_TransactionMultiSource_OfficialSourceOutranksMultipleConfirmations
// for the "official source outranks every confirmation" representative-
// selection assertion this scenario also requires: that test uses a
// PrimarySourceWeight/DomainPriorityWeight-aware RepresentativeConfig (this
// package's clusterTestSettings deliberately zeroes those two weights, per
// its own doc comment, so representative-switch outcomes here are
// predictable by hand from Completeness/Novelty alone — under that
// simplified weighting, a fresher confirmation naming a different
// action/EventType than a several-hours-old plain "signed" report
// legitimately out-scores it on novelty/completeness, so the representative
// switches here even though production's fuller weighting would keep the
// official source in place).
func TestProcessCycle_ClusterTransactionMultiSourceJoinsWithConfirmationRoleAndEvidence(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Officially Confirmed John Smith Signing", Source: source.Source{ID: mustID(t, source.TypeRSS, "third-source"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/john-smith-confirmed", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{
		{
			ArticleID: "existing-official", ClusterID: "cluster-txn",
			Representation: eventsignals.Representation{
				ArticleID: "existing-official", SourceID: "source-1", Domain: "isotopes.example.com",
				PrimaryEntity: "John Smith", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
				EventDate: now.Add(-3 * time.Hour), EventWindow: 48 * time.Hour,
			},
		},
		{
			ArticleID: "existing-confirm", ClusterID: "cluster-txn",
			Representation: eventsignals.Representation{
				ArticleID: "existing-confirm", SourceID: "rival-source", Domain: "rival.com",
				PrimaryEntity: "John Smith", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
				EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
			},
		},
	}}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.created) != 0 {
		t.Errorf("clusters.created = %v, want none: a third source confirming the same transaction should join, not create", clusters.created)
	}
	if len(clusters.addedMember) != 1 {
		t.Fatalf("clusters.addedMember = %+v, want exactly 1 join", clusters.addedMember)
	}
	member := clusters.addedMember[0]
	if member.clusterID != "cluster-txn" {
		t.Errorf("joined clusterID = %q, want cluster-txn", member.clusterID)
	}
	if member.role != clustering.MemberRoleConfirmation {
		t.Errorf("member.role = %v, want %v", member.role, clustering.MemberRoleConfirmation)
	}
	if len(clusters.decisions) != 1 || clusters.decisions[0].decision.Action != clustering.ActionJoin {
		t.Fatalf("clusters.decisions = %+v, want 1 ActionJoin decision", clusters.decisions)
	}
	foundEntityEvidence := false
	for _, e := range clusters.decisions[0].decision.Evidence {
		if strings.Contains(string(e), "John Smith") {
			foundEntityEvidence = true
		}
	}
	if !foundEntityEvidence {
		t.Errorf("decision evidence = %v, want an entity-match line naming John Smith", clusters.decisions[0].decision.Evidence)
	}
}

func TestProcessCycle_ClusterUnrelatedSharedEntityDoesNotJoin(t *testing.T) {
	// A sanity check alongside the "different action" scenario above: two
	// articles sharing an entity but disagreeing on a named opponent must
	// not join either (clustering.detectConflict's opponent-conflict gate).
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Jane Doe Ahead Of Matchup With Sharks", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-vs-sharks", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
		ArticleID: "existing-1", ClusterID: "cluster-existing-1",
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Opponent: "Ogdenville Owls",
			Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}}}
	uc := newProcessCycle(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}
	if len(clusters.addedMember) != 0 {
		t.Errorf("clusters.addedMember = %+v, want none: conflicting opponents must not join", clusters.addedMember)
	}
	if len(clusters.created) != 1 {
		t.Fatalf("clusters.created = %v, want exactly 1 new cluster", clusters.created)
	}
}

func TestProcessCycle_VideoContentTypeExceptionNeverAutoRejectsNearDuplicate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Highlights From Overtime Win Over Owls", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/video-copy", ContentType: article.ContentTypeVideo,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Highlights From An Overtime Win Over Owls", ContentType: article.ContentTypeVideo},
	}}
	uc := newProcessCycleWithCascade(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, func(c *in.DedupCascadeConfig) {
		c.Dedup.ContentTypeExceptions = map[article.ContentType]bool{article.ContentTypeVideo: true}
	})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Duplicate != 0 {
		t.Errorf("result = %+v, want duplicate=0: video is a content_type_exception and must not auto-reject as near-duplicate", result)
	}
}

func TestProcessCycle_ShortTextUsesShortTextThreshold(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Win Thriller Sunday Night Road Trip", Source: source.Source{ID: mustID(t, source.TypeTwitter, "source_1"), Type: source.TypeTwitter},
		OriginalURL: "https://example.com/short-post", ContentType: article.ContentTypeSocialPost,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	similarityIndex := &fakeSimilarityIndexRepository{candidates: []out.Candidate{
		{ArticleID: "existing", ClusterID: "cluster-1", Title: "Isotopes Win Thriller Sunday Night Road Trip Celebration", ContentType: article.ContentTypeSocialPost},
	}}
	outbox := &fakeOutboxRepo{}
	uc := newProcessCycleWithCascade(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, similarityIndex, &fakeClusterRepo{}, &fakeDecisionRepo{}, outbox, &fakeUnitOfWork{}, fakeClock{now: now}, func(c *in.DedupCascadeConfig) {
		c.Dedup.ShortTextMaxTokens = 8
		c.Dedup.ShortTextThreshold = 0.99 // near-impossible to hit by accident
	})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// With an all-but-unreachable short-text threshold, two short,
	// token-similar posts must not be auto-rejected as near-duplicates —
	// this pins down that the short-text override, not the default
	// threshold, governs a low-token-count article.
	if result.Duplicate != 0 {
		t.Errorf("result = %+v, want duplicate=0 under an unreachable short_text_threshold", result)
	}
}

func newProcessCycleWithCascade(
	fetcher fakeFetcher, articles *fakeArticleRepo, dedup *fakeDeduplicationRepo, aliases *fakeAliasRepository,
	similarityIndex *fakeSimilarityIndexRepository, clusters *fakeClusterRepo, decisions *fakeDecisionRepo,
	outbox *fakeOutboxRepo, uow *fakeUnitOfWork, clock fakeClock, tweak func(*in.DedupCascadeConfig),
) *usecase.ProcessCycle {
	cascade := defaultCascade()
	tweak(&cascade)
	return usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, cascade, clock, fakeLogger{}, &fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{}, nil)
}

func TestProcessCycle_RejectsNotRelatedArticleWithoutClustering(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Weather forecast for the weekend", OriginalURL: "https://example.com/weather",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	clusters := &fakeClusterRepo{}
	uc := newProcessCycleWithClassification(fetcher, articles, decisions, clusters, fakeClock{now: now}, classificationMatcher(t), nil, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want rejected=1 accepted=0", result)
	}
	saved := articles.lastSaved()
	if saved.Status != article.StatusRejectedNotRelated {
		t.Errorf("Status = %v, want %v", saved.Status, article.StatusRejectedNotRelated)
	}
	if len(clusters.created) != 0 {
		t.Error("a not-related article must never reach clustering (rejected before candidate generation)")
	}
}

func TestProcessCycle_AcceptsRelatedArticleAndSetsRelevanceScoreAndDictionaryVersion(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Springfield Isotopes sign veteran cornerback", OriginalURL: "https://example.com/signing",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	clusters := &fakeClusterRepo{}
	uc := newProcessCycleWithClassification(fetcher, articles, decisions, clusters, fakeClock{now: now}, classificationMatcher(t), nil, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0", result)
	}
	saved := articles.lastSaved()
	if saved.DictionaryVersion != "test-classification-v1" {
		t.Errorf("DictionaryVersion = %q, want test-classification-v1", saved.DictionaryVersion)
	}
	if saved.ClassifierVersion != "placeholder-v0" {
		t.Errorf("ClassifierVersion = %q, want placeholder-v0 (editorial model is still a placeholder)", saved.ClassifierVersion)
	}
	if saved.RelevanceScore <= 0 {
		t.Errorf("RelevanceScore = %v, want > 0", saved.RelevanceScore)
	}
}

func TestProcessCycle_TrustedSourceBypassesScoringButStillDeduplicatesAndClusters(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Wednesday transactions", OriginalURL: "https://example.com/wed",
		Source: source.Source{ID: mustID(t, source.TypeRSS, "trusted-feed"), Type: source.TypeRSS}, ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	articles := &fakeArticleRepo{}
	decisions := &fakeDecisionRepo{}
	clusters := &fakeClusterRepo{}
	uc := newProcessCycleWithClassification(fetcher, articles, decisions, clusters, fakeClock{now: now}, classificationMatcher(t), map[string]bool{"rss:trusted-feed": true}, nil)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 (a title with zero dictionary evidence would otherwise be rejected)", result)
	}
	if len(clusters.created) != 1 {
		t.Error("a trusted-bypassed article must still go through clustering")
	}
}

func TestProcessCycle_ContextSourceRequiresExplicitTeamEvidence(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	contextID := mustID(t, source.TypeTwitter, "beatreporter")
	articles := &fakeArticleRepo{}
	uc := newProcessCycleWithClassification(fakeFetcher{articles: []article.Article{
		{
			Title: "Isotopes practicing in Napa", OriginalURL: "https://example.com/isotopes",
			Source: source.Source{ID: contextID, Type: source.TypeTwitter}, ContentType: article.ContentTypeSocialPost,
			PublishedAt: now.Add(-time.Hour),
		},
		{
			Title: "Springfield college football notes", OriginalURL: "https://example.com/college",
			Source: source.Source{ID: contextID, Type: source.TypeTwitter}, ContentType: article.ContentTypeSocialPost,
			PublishedAt: now.Add(-30 * time.Minute),
		},
	}}, articles, &fakeDecisionRepo{}, &fakeClusterRepo{}, fakeClock{now: now}, classificationMatcher(t), nil,
		map[string]bool{contextID.String(): true})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 1 {
		t.Fatalf("result = %+v, want one contextual acceptance and one off-topic rejection", result)
	}
}

func TestProcessCycle_ClassificationDisabledKeepsPlaceholderBehavior(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Completely unrelated weather report", OriginalURL: "https://example.com/weather-2",
		ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-1 * time.Hour),
	}}}
	repo := &fakeArticleRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1 (classification disabled must never reject)", result)
	}
	saved := repo.lastSaved()
	if saved.DictionaryVersion != "placeholder-v0" || saved.ClassifierVersion != "placeholder-v0" {
		t.Errorf("versions = %q/%q, want placeholder-v0/placeholder-v0", saved.DictionaryVersion, saved.ClassifierVersion)
	}
}

// fakeMatchStateProvider is a hand-written test double for
// out.MatchStateProvider (no mocking framework in this codebase).
type fakeMatchStateProvider struct {
	state matchstate.State
	ok    bool
	err   error
}

func (f fakeMatchStateProvider) Current(context.Context, time.Time) (matchstate.State, bool, error) {
	return f.state, f.ok, f.err
}

func TestProcessCycle_StampsMatchStateMetadataWhenLiveGameConfirmed(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	repo := &fakeArticleRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})
	uc.SetMatchStateProvider(fakeMatchStateProvider{ok: true, state: matchstate.State{
		Identity: matchstate.Identity{MatchID: "game-1", HomeTeam: "Springfield Isotopes", AwayTeam: "Capital City Capitals"},
		Phase:    matchstate.Q3,
	}})

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := repo.lastSaved()
	want := map[string]string{
		"match_state_kind": "live",
		"match_id":         "game-1",
		"match_home_team":  "Springfield Isotopes",
		"match_away_team":  "Capital City Capitals",
		"match_phase":      "q3",
	}
	for k, v := range want {
		if final.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %q, want %q", k, final.Metadata[k], v)
		}
	}
}

func TestProcessCycle_NoMatchStateProviderLeavesMetadataUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	repo := &fakeArticleRepo{}
	uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})
	// No SetMatchStateProvider call — the nil zero value, exactly like
	// every deployment before this feature existed.

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := repo.lastSaved()
	if len(final.Metadata) != 0 {
		t.Errorf("Metadata = %v, want empty with no MatchStateProvider configured", final.Metadata)
	}
}

func TestProcessCycle_MatchStateProviderNotOKOrErroredLeavesMetadataUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cases := []fakeMatchStateProvider{
		{ok: false}, // no live game right now — the common case
		{ok: false, err: errors.New("espn down")}, // provider error
	}
	for _, provider := range cases {
		fetcher := fakeFetcher{articles: []article.Article{{
			OriginalURL: "https://example.com/story",
			ContentType: article.ContentTypeArticle,
			PublishedAt: now.Add(-1 * time.Hour),
		}}}
		repo := &fakeArticleRepo{}
		uc := newProcessCycle(fetcher, repo, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{}, &fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now})
		uc.SetMatchStateProvider(provider)

		if _, err := uc.Run(context.Background()); err != nil {
			t.Fatalf("Run() error = %v, want nil — a MatchStateProvider failure must never block ingestion", err)
		}
		final := repo.lastSaved()
		if len(final.Metadata) != 0 {
			t.Errorf("provider=%+v: Metadata = %v, want empty", provider, final.Metadata)
		}
	}
}

func (f *fakeEntityProposalRepository) UpdateVerification(ctx context.Context, p entityproposal.Proposal) error {
	return nil
}

// fakeLeagueSource is an in.LeagueEntitySource that records Refresh calls.
type fakeLeagueSource struct {
	entities  []eventsignals.EntityRule
	refreshes int
}

func (f *fakeLeagueSource) Refresh(time.Time)                   { f.refreshes++ }
func (f *fakeLeagueSource) Entities() []eventsignals.EntityRule { return f.entities }

// League entities must be recorded as mentions but change nothing else about
// the stored representation, and must not change the clustering decision.
func TestProcessCycle_LeagueEntitiesDoNotChangeTheClusteringOutcome(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	run := func(src in.LeagueEntitySource) (*fakeClusterRepo, *fakeLeagueSource) {
		fetcher := fakeFetcher{articles: []article.Article{{
			Title: "Isotopes Sign Jane Doe as Josh Wrenfield watches", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
			OriginalURL: "https://example.com/jane-doe-signing", ContentType: article.ContentTypeArticle,
			PublishedAt: now.Add(-1 * time.Hour),
		}}}
		clusters := &fakeClusterRepo{candidates: []clustering.Candidate{{
			ArticleID: "existing-1", ClusterID: "cluster-existing-1",
			Representation: eventsignals.Representation{
				ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
				PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
				EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
			},
		}}}
		uc := newProcessCycleWithCascade(fetcher, &fakeArticleRepo{}, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
			clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, fakeClock{now: now},
			func(c *in.DedupCascadeConfig) { c.Cluster.LeagueEntities = src })
		if _, err := uc.Run(context.Background()); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		fs, _ := src.(*fakeLeagueSource)
		return clusters, fs
	}

	off, _ := run(nil)
	league := &fakeLeagueSource{entities: []eventsignals.EntityRule{{
		ID: "league-josh-wrenfield", CanonicalName: "Josh Wrenfield", Kind: eventsignals.EntityKindPlayer, Scope: eventsignals.ScopeLeague,
		Terms: []string{"josh wrenfield"}, Prefilter: eventsignals.PrefilterWords("josh wrenfield"),
	}}}
	on, src := run(league)

	if src.refreshes != 1 {
		t.Errorf("Refresh called %d times, want once per Run", src.refreshes)
	}
	if len(on.addedMember) != len(off.addedMember) || len(on.created) != len(off.created) ||
		len(on.decisions) != len(off.decisions) || on.decisions[0].decision.Action != off.decisions[0].decision.Action {
		t.Fatalf("clustering outcome changed: off=%+v/%+v on=%+v/%+v", off.addedMember, off.decisions, on.addedMember, on.decisions)
	}
	if len(on.representations) != 1 || len(off.representations) != 1 {
		t.Fatalf("representations: off=%d on=%d, want 1 each", len(off.representations), len(on.representations))
	}
	a, b := off.representations[0], on.representations[0]
	if a.PrimaryEntity != b.PrimaryEntity || a.Opponent != b.Opponent || a.PrimaryTeam != b.PrimaryTeam || !reflect.DeepEqual(a.ApproximateKeys, b.ApproximateKeys) {
		t.Fatalf("clustering inputs changed:\n off: %+v\n  on: %+v", a, b)
	}
	var seen bool
	for _, m := range b.EntityMentions {
		seen = seen || (m.ID == "league-josh-wrenfield" && m.Scope == eventsignals.ScopeLeague)
	}
	if !seen {
		t.Fatalf("EntityMentions = %+v, want the league mention recorded", b.EntityMentions)
	}
}
