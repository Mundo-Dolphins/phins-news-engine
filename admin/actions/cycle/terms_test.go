package cycle_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newTermsFixture is a cycle at the mined-terms step of a review session
// whose one published article mines the terms of the CLI's golden test
// ("Ezo Quarrington" guessed nfl_people_context, ten more with no guess).
func newTermsFixture(t *testing.T) (f *fixture, dictPath string) {
	t.Helper()
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.OpenImmediate(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		t.Fatal(err)
	}
	f = &fixture{t: t, cfg: cfg, db: db, storePath: filepath.Join(t.TempDir(), "admin.db"), log: &bytes.Buffer{}}
	f.openStore()
	t.Cleanup(func() {
		_ = f.db.Close()
		_ = f.st.Close()
	})
	seedSessionDecisions(t, db, "sess-1", nil)
	saveMiningArticle(t, db)
	f.mustRun("cycle.start", `{}`)
	f.setState(func(m map[string]any) { m["step"], m["session_id"] = "mined_terms", "sess-1" })
	return f, dictPath
}

type termsQueue struct {
	Items []struct {
		Term             string `json:"term"`
		Support          int    `json:"support"`
		FirstSeenArticle struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"first_seen_article"`
		CategoryGuess string `json:"category_guess"`
		Decision      *struct {
			Status   string `json:"status"`
			Category string `json:"category"`
		} `json:"decision"`
	} `json:"items"`
	Categories []string `json:"categories"`
	Undecided  int      `json:"undecided"`
}

func (f *fixture) termsQueue() termsQueue {
	f.t.Helper()
	return decode[termsQueue](f.t, f.mustRun("cycle.terms_queue", `{}`))
}

func dictionaryVersion(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "version:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "version:")), `"`)
		}
	}
	t.Fatalf("no version in %s", raw)
	return ""
}

func TestTermsPrepareAndQueue(t *testing.T) {
	f, _ := newTermsFixture(t)
	_, err := f.run("cycle.terms_queue", `{}`)
	wantConflict(t, err, "prepare the mined terms first")
	f.mustRun("cycle.terms_prepare", `{}`)
	q := f.termsQueue()
	if len(q.Items) != 11 || q.Undecided != 11 {
		t.Fatalf("queue = %+v", q)
	}
	first := q.Items[0]
	if first.Term != "Ezo Quarrington" || first.Support != 2 || first.CategoryGuess != "nfl_people_context" || first.Decision != nil {
		t.Errorf("first = %+v", first)
	}
	if first.FirstSeenArticle.ID != "a1" || first.FirstSeenArticle.Title != "Ezo Quarrington leads training camp session" ||
		first.FirstSeenArticle.URL != "https://example.com/a1" {
		t.Errorf("first seen = %+v", first.FirstSeenArticle)
	}
	if strings.Join(q.Categories, ",") != "coaching_staff,nfl_people_context,official_names" {
		t.Errorf("categories = %v", q.Categories)
	}
}

func TestTermsApproveNeedsCategory(t *testing.T) {
	f, _ := newTermsFixture(t)
	f.mustRun("cycle.terms_prepare", `{}`)
	_, err := f.run("cycle.terms_decide", `{"term":"ezo","action":"approve"}`)
	wantInvalid(t, err, "choose a category")
	_, err = f.run("cycle.terms_decide", `{"term":"ezo","action":"categorize"}`)
	wantInvalid(t, err, "choose a category")
	_, err = f.run("cycle.terms_decide", `{"term":"ezo","action":"approve","category":"nowhere"}`)
	wantInvalid(t, err, "not one of the dictionary's categories")
	f.mustRun("cycle.terms_decide", `{"term":"ezo","action":"approve","category":"coaching_staff"}`)
	f.mustRun("cycle.terms_decide", `{"term":"Ezo Quarrington","action":"approve"}`)
	f.mustRun("cycle.terms_decide", `{"term":"camp","action":"categorize","category":"official_names"}`)
	f.mustRun("cycle.terms_decide", `{"term":"leads","action":"reject"}`)
	_, err = f.run("cycle.terms_decide", `{"term":"unknown","action":"reject"}`)
	if err == nil {
		t.Fatal("unknown term accepted")
	}
	want := map[string]string{"ezo": "approved/coaching_staff", "Ezo Quarrington": "approved/nfl_people_context", "camp": "approved/official_names", "leads": "rejected/"}
	q := f.termsQueue()
	for _, it := range q.Items {
		got := ""
		if it.Decision != nil {
			got = it.Decision.Status + "/" + it.Decision.Category
			if it.Decision.Status == "rejected" {
				got = "rejected/"
			}
		}
		if got != want[it.Term] {
			t.Errorf("%s: decision = %q, want %q", it.Term, got, want[it.Term])
		}
	}
	if q.Undecided != 7 {
		t.Errorf("undecided = %d", q.Undecided)
	}
}

func TestTermsFinishRefusesUndecided(t *testing.T) {
	f, dict := newTermsFixture(t)
	f.mustRun("cycle.terms_prepare", `{}`)
	f.mustRun("cycle.terms_decide", `{"term":"Ezo Quarrington","action":"approve"}`)
	_, err := f.run("cycle.terms_finish", `{}`)
	wantConflict(t, err, "10 term(s) still undecided")
	if v := dictionaryVersion(t, dict); v != "1.0.0" {
		t.Errorf("version = %s, want the dictionary untouched", v)
	}
	if _, err := f.run("cycle.next", `{}`); err == nil {
		t.Fatal("next accepted with undecided terms")
	}
}

func TestTermsFinishWritesOnce(t *testing.T) {
	f, dict := newTermsFixture(t)
	f.mustRun("cycle.terms_prepare", `{}`)
	q := f.termsQueue()
	for _, it := range q.Items {
		act := "reject"
		if it.Term == "Ezo Quarrington" {
			act = "approve"
		}
		f.mustRun("cycle.terms_decide", fmt.Sprintf(`{"term":%q,"action":%q}`, it.Term, act))
	}
	// Leaving and coming back keeps the decisions: preparing again offers
	// nothing new and the queue still lists every term.
	f.mustRun("cycle.terms_prepare", `{}`)
	if q := f.termsQueue(); len(q.Items) != 11 || q.Undecided != 0 {
		t.Fatalf("queue after re-prepare = %+v", q)
	}
	f.mustRun("cycle.terms_finish", `{}`)
	if v := dictionaryVersion(t, dict); v != "1.0.1" {
		t.Fatalf("version = %s, want 1.0.1", v)
	}
	s := f.state().Cycle.Steps["mined_terms"]
	var r struct {
		Added   int    `json:"added"`
		Entries int    `json:"entries"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		t.Fatal(err)
	}
	if s.Status != "done" || r.Added != 1 || r.Entries != 1 || r.From != "1.0.0" || r.To != "1.0.1" {
		t.Fatalf("step = %+v %+v", s, r)
	}
	_, err := f.run("cycle.terms_finish", `{}`)
	wantConflict(t, err, "already finished")
	_, err = f.run("cycle.terms_decide", `{"term":"camp","action":"approve","category":"official_names"}`)
	wantConflict(t, err, "already finished")
	if v := dictionaryVersion(t, dict); v != "1.0.1" {
		t.Fatalf("version after second finish = %s, want 1.0.1", v)
	}
}

// saveMiningArticle stores the published article of session sess-1, as the
// CLI's mined-term tests seed it.
func saveMiningArticle(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Title: "Ezo Quarrington leads training camp session", FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewReviewDecisionRepository(db).Save(ctx, out.ReviewDecision{
		SessionID: "sess-1", ArticleID: "a1", Action: "publish", Label: "publish", DecidedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewHumanLabelRepository(db).Label(ctx, training.Example{ArticleID: "a1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatal(err)
	}
}
