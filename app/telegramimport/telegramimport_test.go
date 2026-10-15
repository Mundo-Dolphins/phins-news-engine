package telegramimport_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	domaintelegramimport "github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

// fakeArticles fakes ports/out.ArticleRepository. errByURL lets a test
// inject an arbitrary repository failure (locked, corrupt, generic, ...)
// for a specific canonical URL; a URL with no entry in byURL and no
// injected error behaves like a genuine "not found" — wrapping
// article.ErrNotFound, exactly as the sqlite adapter does.
type fakeArticles struct {
	byURL    map[string]article.Article
	errByURL map[string]error
}

func (f *fakeArticles) Save(context.Context, article.Article) error { return nil }
func (f *fakeArticles) FindByID(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

func (f *fakeArticles) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticles) Count(context.Context) (int, error) { return 0, nil }
func (f *fakeArticles) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) FindByCanonicalURL(_ context.Context, url string) (article.Article, error) {
	if err, ok := f.errByURL[url]; ok {
		return article.Article{}, err
	}
	a, ok := f.byURL[url]
	if !ok {
		return article.Article{}, fmt.Errorf("fake: no article for %s: %w", url, article.ErrNotFound)
	}
	return a, nil
}

// fakeLabels fakes ports/out.HumanLabelRepository. Save mutates byArticle
// too (not just the saved log), so a second Import call against the same
// fake sees earlier labels as already-known — modeling the real
// human_labels-backed idempotency check that makes re-running Import safe.
type fakeLabels struct {
	byArticle map[string][]training.Example
	saved     []training.Example
	failSave  bool
}

func (f *fakeLabels) Label(_ context.Context, e training.Example) error {
	if f.failSave {
		return fmt.Errorf("Label should not have been called in dry-run mode")
	}
	f.saved = append(f.saved, e)
	if f.byArticle == nil {
		f.byArticle = map[string][]training.Example{}
	}
	f.byArticle[e.ArticleID] = append(f.byArticle[e.ArticleID], e)
	return nil
}

func (f *fakeLabels) ListByArticle(_ context.Context, articleID string) ([]training.Example, error) {
	return f.byArticle[articleID], nil
}
func (f *fakeLabels) ListAll(context.Context) ([]training.Example, error)  { return nil, nil }
func (f *fakeLabels) Relabel(context.Context, training.Example, int) error { return nil }
func (f *fakeLabels) Retract(context.Context, string, int) error           { return nil }
func (f *fakeLabels) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (f *fakeLabels) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (f *fakeLabels) CountUnresolvedConflicts(context.Context) (int, error)           { return 0, nil }
func (f *fakeLabels) ResolveConflict(context.Context, string, training.Example) error { return nil }
func (f *fakeLabels) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}
func (f *fakeLabels) CountEditorialExamples(context.Context) (int, error) { return 0, nil }

// testURLConfig returns a minimal normalization.URLConfig with no
// tracking-param stripping or link resolvers — fine for these tests,
// which use already-clean URLs.
func testURLConfig() (normalization.URLConfig, []normalization.KnownLinkResolver) {
	return normalization.URLConfig{}, nil
}

func TestImport_EmptyMessageList_ReturnsZeroResult(t *testing.T) {
	deps := telegramimport.Deps{Articles: &fakeArticles{byURL: map[string]article.Article{}}, Labels: &fakeLabels{byArticle: map[string][]training.Example{}}, Clock: fakeClock{now: time.Now()}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, nil, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result != (telegramimport.Result{}) {
		t.Errorf("result = %+v, want the zero Result", result)
	}
}

func TestImport_MatchesAndLabelsNewArticle(t *testing.T) {
	articles := &fakeArticles{byURL: map[string]article.Article{
		"https://example.com/a1": {ID: "a1"},
	}}
	labels := &fakeLabels{byArticle: map[string][]training.Example{}}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}

	messages := []domaintelegramimport.Message{
		{ID: 1, Text: "Isotopes sign veteran — https://example.com/a1"},
	}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Imported != 1 || result.Processed != 1 {
		t.Errorf("result = %+v, want Imported=1 Processed=1", result)
	}
	if len(labels.saved) != 1 || labels.saved[0].ArticleID != "a1" || labels.saved[0].Label != "publish" || labels.saved[0].Reason != "historical_import" {
		t.Errorf("labels.saved = %+v, want one publish/historical_import label for a1", labels.saved)
	}
}

func TestImport_SkipsAlreadyLabeledArticle(t *testing.T) {
	articles := &fakeArticles{byURL: map[string]article.Article{
		"https://example.com/a1": {ID: "a1"},
	}}
	labels := &fakeLabels{byArticle: map[string][]training.Example{
		"a1": {{ArticleID: "a1", Label: "publish"}},
	}}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}

	messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/a1"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.AlreadyKnown != 1 || result.Imported != 0 {
		t.Errorf("result = %+v, want AlreadyKnown=1 Imported=0", result)
	}
	if len(labels.saved) != 0 {
		t.Errorf("labels.saved = %+v, want no new label written for an already-labeled article", labels.saved)
	}
}

func TestImport_NoURLInMessageCountsAsSkipped(t *testing.T) {
	deps := telegramimport.Deps{Articles: &fakeArticles{byURL: map[string]article.Article{}}, Labels: &fakeLabels{byArticle: map[string][]training.Example{}}, Clock: fakeClock{now: time.Now()}}
	messages := []domaintelegramimport.Message{{ID: 1, Text: "Just an announcement, no link here"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Skipped != 1 || result.Unmatched != 0 {
		t.Errorf("result = %+v, want Skipped=1 Unmatched=0 (no URL is not the same as \"not found\")", result)
	}
}

func TestImport_UnparseableURLCountsAsSkipped(t *testing.T) {
	deps := telegramimport.Deps{Articles: &fakeArticles{byURL: map[string]article.Article{}}, Labels: &fakeLabels{byArticle: map[string][]training.Example{}}, Clock: fakeClock{now: time.Now()}}
	// A malformed percent-escape makes url.Parse (inside normalization.NormalizeURL) fail.
	messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/%zz"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Skipped != 1 || result.Unmatched != 0 {
		t.Errorf("result = %+v, want Skipped=1 Unmatched=0 (a normalize failure is not the same as \"not found\")", result)
	}
}

func TestImport_URLNotFoundLocallyCountsAsUnmatched(t *testing.T) {
	deps := telegramimport.Deps{Articles: &fakeArticles{byURL: map[string]article.Article{}}, Labels: &fakeLabels{byArticle: map[string][]training.Example{}}, Clock: fakeClock{now: time.Now()}}
	messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/never-seen"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Unmatched != 1 {
		t.Errorf("result = %+v, want Unmatched=1", result)
	}
}

func TestImport_DryRunNeverCallsSave(t *testing.T) {
	articles := &fakeArticles{byURL: map[string]article.Article{
		"https://example.com/a1": {ID: "a1"},
	}}
	labels := &fakeLabels{byArticle: map[string][]training.Example{}, failSave: true}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}

	messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/a1"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, true)
	if err != nil {
		t.Fatalf("Import() error = %v (dry-run must never call Save, which is rigged to fail here)", err)
	}
	if result.Imported != 1 {
		t.Errorf("result = %+v, want Imported=1 even in dry-run (the count reflects what WOULD happen)", result)
	}
}

// errClassification exercises every non-notfound repository failure class
// the acceptance criteria calls out by name: a wrapped sql.ErrNoRows-alike
// ("database locked"), a generic driver error, and (in a dedicated test
// below) context cancellation. None of these may ever be classified as
// Unmatched — all three currently collapse into Unmatched before this
// change; these tests pin the corrected behavior.
func errClassificationCases() []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{"locked", errors.New("database is locked (5) (SQLITE_BUSY)")},
		{"corrupt", errors.New("file is not a database (26) (SQLITE_NOTADB)")},
		{"generic", errors.New("boom")},
	}
}

func TestImport_NonNotFoundRepositoryErrorAbortsAndPropagates(t *testing.T) {
	for _, tc := range errClassificationCases() {
		t.Run(tc.name, func(t *testing.T) {
			articles := &fakeArticles{
				byURL:    map[string]article.Article{},
				errByURL: map[string]error{"https://example.com/bad": tc.err},
			}
			labels := &fakeLabels{byArticle: map[string][]training.Example{}}
			deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}

			messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/bad"}}
			urlCfg, resolvers := testURLConfig()
			result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
			if err == nil {
				t.Fatalf("Import() error = nil, want an error propagated from the repository (%v)", tc.err)
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("Import() error = %v, want it to wrap %v", err, tc.err)
			}
			if result.Unmatched != 0 {
				t.Errorf("result = %+v, want Unmatched=0 — a real repository error must never be counted as \"not found\"", result)
			}
			if result.Failed != 1 {
				t.Errorf("result = %+v, want Failed=1", result)
			}
		})
	}
}

func TestImport_PartialFailureReturnsPartialResultNotZeroValue(t *testing.T) {
	articles := &fakeArticles{
		byURL:    map[string]article.Article{"https://example.com/ok": {ID: "a1"}},
		errByURL: map[string]error{"https://example.com/bad": errors.New("database is locked")},
	}
	labels := &fakeLabels{byArticle: map[string][]training.Example{}}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}

	messages := []domaintelegramimport.Message{
		{ID: 1, Text: "https://example.com/ok"},
		{ID: 2, Text: "https://example.com/bad"},
		{ID: 3, Text: "https://example.com/ok"}, // never reached: fail-fast aborts at message 2
	}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err == nil {
		t.Fatal("Import() error = nil, want an error from the second message's repository failure")
	}
	if result.Processed != 2 {
		t.Errorf("result.Processed = %d, want 2 (fail-fast stops before message 3)", result.Processed)
	}
	if result.Imported != 1 {
		t.Errorf("result.Imported = %d, want 1 (message 1 succeeded before the abort)", result.Imported)
	}
	if result.Failed != 1 {
		t.Errorf("result.Failed = %d, want 1", result.Failed)
	}
	if len(labels.saved) != 1 {
		t.Errorf("labels.saved = %+v, want exactly the one label written before the abort", labels.saved)
	}
}

func TestImport_ContextCanceledAbortsAndPropagates(t *testing.T) {
	articles := &fakeArticles{byURL: map[string]article.Article{"https://example.com/a1": {ID: "a1"}}}
	labels := &fakeLabels{byArticle: map[string][]training.Example{}}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	messages := []domaintelegramimport.Message{{ID: 1, Text: "https://example.com/a1"}}
	urlCfg, resolvers := testURLConfig()
	result, err := telegramimport.Import(ctx, deps, messages, urlCfg, resolvers, false)
	if err == nil {
		t.Fatal("Import() error = nil, want context.Canceled to propagate")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Import() error = %v, want it to wrap context.Canceled", err)
	}
	if result.Processed != 0 || result.Imported != 0 {
		t.Errorf("result = %+v, want no messages processed — the context was already canceled", result)
	}
}

func TestImport_ReRunAfterPartialFailureDoesNotDuplicateLabels(t *testing.T) {
	articles := &fakeArticles{
		byURL:    map[string]article.Article{"https://example.com/ok": {ID: "a1"}},
		errByURL: map[string]error{"https://example.com/bad": errors.New("database is locked")},
	}
	labels := &fakeLabels{byArticle: map[string][]training.Example{}}
	deps := telegramimport.Deps{Articles: articles, Labels: labels, Clock: fakeClock{now: time.Now()}}
	urlCfg, resolvers := testURLConfig()

	messages := []domaintelegramimport.Message{
		{ID: 1, Text: "https://example.com/ok"},
		{ID: 2, Text: "https://example.com/bad"},
	}
	if _, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false); err == nil {
		t.Fatal("first Import() error = nil, want the injected repository failure")
	}
	if len(labels.saved) != 1 {
		t.Fatalf("after first run, labels.saved = %+v, want 1 entry", labels.saved)
	}

	// Fix the transient failure (as an operator re-running the command
	// after a locked/corrupt DB is fixed would see) and re-run over the
	// same message list.
	delete(articles.errByURL, "https://example.com/bad")
	articles.byURL["https://example.com/bad"] = article.Article{ID: "a2"}

	result, err := telegramimport.Import(context.Background(), deps, messages, urlCfg, resolvers, false)
	if err != nil {
		t.Fatalf("second Import() error = %v", err)
	}
	if result.AlreadyKnown != 1 {
		t.Errorf("result = %+v, want AlreadyKnown=1 for the message already labeled by the first run", result)
	}
	if result.Imported != 1 {
		t.Errorf("result = %+v, want Imported=1 for the message that failed the first time", result)
	}
	if len(labels.saved) != 2 {
		t.Errorf("labels.saved = %+v, want 2 total (no duplicate for a1)", labels.saved)
	}
	a1Count := 0
	for _, e := range labels.saved {
		if e.ArticleID == "a1" {
			a1Count++
		}
	}
	if a1Count != 1 {
		t.Errorf("a1 labeled %d times across both runs, want exactly 1 (re-running must not duplicate)", a1Count)
	}
}

func TestExtractURL(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"bare url", "Big news https://example.com/a/b?x=1 today", "https://example.com/a/b?x=1", true},
		{"no url", "no link here", "", false},
		{"leading url", "https://example.com/x", "https://example.com/x", true},
	}
	for _, tt := range tests {
		got, ok := telegramimport.ExtractURL(tt.text)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ExtractURL(%q) = (%q, %v), want (%q, %v)", tt.text, got, ok, tt.want, tt.ok)
		}
	}
}
