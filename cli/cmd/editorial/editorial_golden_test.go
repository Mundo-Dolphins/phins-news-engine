package editorial_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// These golden tests pin the full output (stdout and stderr, in order) of
// the editorial reporting commands and the LLM teacher runs, so moving
// their logic out of the CLI cannot change a single byte.

func assertGolden(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

const canaryNote = "(2 of 4 configured criteria evaluated: article count, elapsed days; precision and false-positive-rate require labeled ground truth not yet available)"

func TestEditorialCanaryGolden_StatusPass(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	withCanaryNow(t, fixedNow)
	seedCanaryEvents(t, configPath, canaryPassingFixtureEvents(fixedNow))

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "status")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	assertGolden(t, got, "PASS: articles=100 elapsed_days=8 precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)\n"+
		"  "+canaryNote+"\n")
}

func TestEditorialCanaryGolden_StatusFailAndPromote(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	withCanaryNow(t, fixedNow)
	seedCanaryEvents(t, configPath, canaryFailingFixtureEvents(fixedNow))

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "status")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	assertGolden(t, got, "FAIL: articles=3 elapsed_days=0 precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)\n"+
		"  "+canaryNote+"\n"+
		"  - article count 3 < min 100\n"+
		"  - elapsed days 0 < min 7\n")

	got, err = testsupport.Execute(t, "--config", configPath, "editorial", "canary", "promote")
	if err == nil {
		t.Fatal("promote without --force: error = nil")
	}
	assertGolden(t, got, "Error: editorial canary promote: criteria failing ([article count 3 < min 100 elapsed days 0 < min 7]); re-run with --force to override\n")

	got, err = testsupport.Execute(t, "--config", configPath, "editorial", "canary", "promote", "--force")
	if err != nil {
		t.Fatalf("promote --force error = %v, output: %s", err, got)
	}
	assertGolden(t, got, "promotion approved (forced=true) — update editorial.mode: enforce in "+configPath+" and deploy via `deploy run` / `editorial cycle`\n"+
		"  articles=3 elapsed_days=0 precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)\n"+
		"  "+canaryNote+"\n")
}

// metricsSinceRE matches the clock-derived since value, the only part of
// the metrics output a test cannot pin.
var metricsSinceRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)

func seedGoldenMetrics(t *testing.T, configPath string) {
	t.Helper()
	now := time.Now().UTC()
	seedEditorialEvents(t, configPath, []out.Event{
		{ArticleID: "g1", EventType: "editorial", ReasonCode: "editorial:reject_score", Mode: "enforce", Score: 5, OccurredAt: now},
		{ArticleID: "g2", EventType: "editorial_ambiguous", Mode: "enforce", Score: 45, OccurredAt: now},
	})
}

func TestEditorialMetricsGolden_Text(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	seedGoldenMetrics(t, configPath)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "metrics", "--since", "24h")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	if n := len(metricsSinceRE.FindAllString(got, -1)); n != 1 {
		t.Fatalf("output %q: %d since timestamps, want 1", got, n)
	}
	assertGolden(t, metricsSinceRE.ReplaceAllString(got, "SINCE"),
		"since=SINCE total=2 reject_rate=0.500 ambiguous_rate=0.500\n"+
			"  mode=enforce count=2\n"+
			"  reason_code=editorial:reject_score count=1\n")
}

func TestEditorialMetricsGolden_JSON(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	seedGoldenMetrics(t, configPath)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "metrics", "--since", "24h", "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	assertGolden(t, metricsSinceRE.ReplaceAllString(got, "SINCE"), `{
  "since": "SINCE",
  "total": 2,
  "by_mode": {
    "enforce": 2
  },
  "by_reason_code": {
    "editorial:reject_score": 1
  },
  "reject_rate": 0.5,
  "ambiguous_rate": 0.5,
  "by_score_decile": [
    1,
    0,
    0,
    0,
    1,
    0,
    0,
    0,
    0,
    0
  ]
}
`)
}

func TestEditorialReviewGolden(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: "r1", Title: "Isotopes sign big extension", OriginalURL: "https://example.com/r1", CanonicalURL: "https://example.com/r1",
		FetchedAt: at, PublishedAt: at, Status: article.StatusPendingPublication,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	// The second event references an article that is later deleted, so
	// the report falls back to its "(article not found)" row.
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: "r2", Title: "gone", OriginalURL: "https://example.com/r2", CanonicalURL: "https://example.com/r2",
		FetchedAt: at, PublishedAt: at, Status: article.StatusFetched,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	decisions := sqliteadapter.NewDecisionRepository(db)
	for _, e := range []out.Event{
		{ArticleID: "r1", OccurredAt: at, EventType: "editorial_shadow", Score: 82.5},
		{ArticleID: "r2", OccurredAt: at.Add(-time.Minute), EventType: "editorial", Score: 3},
	} {
		if err := decisions.Append(context.Background(), e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF; DELETE FROM articles WHERE id = 'r2'`); err != nil {
		t.Fatalf("delete article: %v", err)
	}

	got, err := testsupport.Execute(t, "--config", configPath, "editorial-review", "--days", "7", "--limit", "50")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	ts := at.Format(time.RFC3339)
	ts2 := at.Add(-time.Minute).Format(time.RFC3339)
	assertGolden(t, got, "editorial decisions in the last 7 days (limit 50): 2 total\n"+
		"  editorial_shadow: 1\n"+
		"  editorial: 1\n"+
		ts+"  editorial_shadow     score=82.5  actual_status=pending_publication       Isotopes sign big extension\n"+
		ts2+"  editorial            score=3.0  actual_status=(unknown)                 (article not found)\n")

	got, err = testsupport.Execute(t, "--config", testsupport.WriteFixture(t), "editorial-review")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertGolden(t, got, "editorial decisions in the last 7 days (limit 50): 0 total\n")
}

func writeGoldenFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const goldenVetoConfig = `version: "1"
rules:
  - category: noise
    reason_code: "veto:noise:spotted"
    patterns: ["isotopes spotted"]
    exclude_patterns: ["NFL"]
  - category: gambling
    reason_code: "veto:gambling:promo"
    patterns: ["free bets", "odds boost"]
`

const goldenDataset = `examples:
  - article_id: v1
    title: Isotopes spotted near the coast
    source_id: "rss:test"
    label: reject
    label_source: human
  - article_id: v2
    title: Isotopes spotted at NFL practice
    source_id: "rss:test"
    label: publish
    label_source: human
  - article_id: v3
    title: Free bets and an odds boost before kickoff
    source_id: "rss:test"
    label: reject
    label_source: human
  - article_id: v4
    title: Coach press conference
    source_id: "rss:test"
    label: publish
    label_source: telegramimport
`

func TestEditorialVetoReportGolden(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	vetoPath := writeGoldenFile(t, "veto.yml", goldenVetoConfig)
	datasetPath := writeGoldenFile(t, "dataset.yml", goldenDataset)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "veto-report", "--dataset", datasetPath, "--veto-config", vetoPath)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	assertGolden(t, got, "dataset="+datasetPath+" total=4 blocked=2 reduction=0.500\n"+
		"  reason_code=veto:gambling:promo blocked=1 examples=[v3]\n"+
		"  reason_code=veto:noise:spotted blocked=1 examples=[v1]\n")

	got, err = testsupport.Execute(t, "--config", configPath, "editorial", "veto-report", "--dataset", datasetPath, "--veto-config", vetoPath, "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	wantJSON, _ := json.Marshal(datasetPath)
	assertGolden(t, got, `{"dataset":`+string(wantJSON)+`,"total":4,"blocked":2,"reduction":0.5,"by_reason_code":{"veto:gambling:promo":1,"veto:noise:spotted":1},"examples_by_reason_code":{"veto:gambling:promo":["v3"],"veto:noise:spotted":["v1"]}}`+"\n")
}

// writeLLMFixture is the standard fixture with offline_training.llm given a
// timeout, so the teacher commands get past their config checks.
func writeLLMFixture(t *testing.T) string {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "offline_training:\n  llm:\n    auto_label_threshold: 0.95\n"
	if !strings.Contains(string(raw), anchor) {
		t.Fatal("fixture has no offline_training.llm block to extend")
	}
	patched := strings.Replace(string(raw), anchor, anchor+"    timeout_seconds: 5\n", 1)
	if err := os.WriteFile(configPath, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// teacherServer answers chat-completion requests: the first call with
// labelsJSON, every later call with unparseable content (a failed batch).
func teacherServer(t *testing.T, labelsJSON string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		content := "not json"
		if calls.Add(1) == 1 {
			content = labelsJSON
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEditorialClassifyGolden_MissingBaseURL(t *testing.T) {
	got, err := testsupport.Execute(t, "--config", testsupport.WriteFixture(t), "editorial", "classify")
	if err == nil {
		t.Fatal("error = nil")
	}
	assertGolden(t, got, "Error: editorial classify: offline_training.llm.base_url is required (or pass --llm-base-url)\n")
}

func TestEditorialClassifyGolden_FailedBatch(t *testing.T) {
	configPath := writeLLMFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	articles := sqliteadapter.NewArticleRepository(db)
	for i, id := range []string{"c1", "c2", "c3"} {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := articles.Save(context.Background(), article.Article{
			ID: id, Title: "Story " + id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			FetchedAt: at, PublishedAt: at, Status: article.StatusFetched,
			Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
		}); err != nil {
			t.Fatalf("seed article: %v", err)
		}
	}
	db.Close()
	srv := teacherServer(t, `{"labels": [{"article_id": "c1", "label": "publish", "confidence": 0.9, "reason": "on topic"}, {"article_id": "zz", "label": "publish", "confidence": 0.9, "reason": "stray"}]}`)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "classify", "--since", "24h", "--batch-size", "2",
		"--llm-base-url", srv.URL, "--llm-model", "test-model")
	if err == nil {
		t.Fatal("error = nil, want the failed-batch error")
	}
	assertGolden(t, got, "articles considered: 3\n"+
		"batches: 2 (failed: 1)\n"+
		"proposals saved: 1 (skipped, pending exists: 0, labels missing: 1)\n"+
		goldenBatchErrorLine+
		"  label error: editorialteacher: article_id \"zz\" not in requested batch\n"+
		"review queue sync skipped: editorial.mode is disabled (no promoted editorial model expected)\n"+
		"Error: editorial classify: 1 of 2 batches failed\n")
}

func TestEditorialClassifyEvalGolden(t *testing.T) {
	configPath := writeLLMFixture(t)
	datasetPath := writeGoldenFile(t, "dataset.yml", goldenDataset)
	missesPath := filepath.Join(t.TempDir(), "misses.csv")
	srv := teacherServer(t, `{"labels": [{"article_id": "v1", "label": "discard", "confidence": 0.97, "reason": "noise"}, {"article_id": "v2", "label": "discard", "confidence": 0.96, "reason": "wrong"}, {"article_id": "v3", "label": "discard", "confidence": 0.5, "reason": "promo"}]}`)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "classify-eval", "--dataset", datasetPath, "--fold", "all",
		"--threshold", "0.9", "--misses-out", missesPath, "--llm-base-url", srv.URL, "--llm-model", "test-model")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	assertGolden(t, got, "evaluating 3 examples (fold=all) with test-model\n"+
		"batches: 1 (failed: 0), labels missing: 0, uncertain: 0\n"+
		"human labels: 2 reject, 1 publish\n"+
		"discards (any confidence): 3, 2 were human rejects\n"+
		"confident discards (>= 0.90): 2, 1 were human rejects -> precision 0.500\n"+
		"human rejects caught confidently: 1/2 -> recall 0.500\n"+
		"human publishes confidently discarded: 1/1\n"+
		"misses written to "+missesPath+"\n")
	raw, err := os.ReadFile(missesPath)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, string(raw), "article_id,confidence,domain,title,llm_reason\nv2,0.96,,Isotopes spotted at NFL practice,wrong\n")

	got, err = testsupport.Execute(t, "--config", configPath, "editorial", "classify-eval", "--dataset", datasetPath, "--fold", "all",
		"--sample", "1", "--llm-base-url", srv.URL, "--llm-model", "test-model", "--include-imported", "--batch-size", "1")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	if !strings.HasPrefix(got, "evaluating 1 examples (fold=all) with test-model\nbatches: 1 (failed: 1), labels missing: 1,") {
		t.Errorf("failed-batch eval output = %q", got)
	}

	emptyPath := writeGoldenFile(t, "empty.yml", "examples: []\n")
	got, err = testsupport.Execute(t, "--config", configPath, "editorial", "classify-eval", "--dataset", emptyPath,
		"--llm-base-url", srv.URL, "--llm-model", "test-model")
	if err == nil {
		t.Fatal("empty dataset: error = nil")
	}
	assertGolden(t, got, "Error: editorial classify-eval: no examples selected from "+emptyPath+"\n")
}

// goldenBatchErrorLine is the batch error the unparseable teacher reply produces.
const goldenBatchErrorLine = "  batch error: llmclient: parse response content: invalid JSON: invalid character 'o' in literal null (expecting 'u')\n"
