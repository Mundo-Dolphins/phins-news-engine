// app/entitycycle/run_test.go
package entitycycle_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type scriptedLLM struct {
	entities []appout.EntityDiscoveryEntity
	err      error
	calls    int
}

func (l *scriptedLLM) Discover(_ context.Context, _ []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	l.calls++
	if l.err != nil {
		return appout.EntityDiscoveryResult{}, l.err
	}
	return appout.EntityDiscoveryResult{Entities: l.entities}, nil
}

func (l *scriptedLLM) VerifyCandidate(context.Context, appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionFinal}, nil
}

type env struct {
	deps    entitycycle.Deps
	llm     *scriptedLLM
	repo    *sqliteentityproposal.EntityProposalRepository
	applied []string
	regen   int
	ids     []string
}

func seedArticle(t *testing.T, repo *sqliteadapter.ArticleRepository, id, title string, fetched time.Time) {
	t.Helper()
	sid, err := source.NewID(source.TypeRSS, "test-source")
	if err != nil {
		t.Fatal(err)
	}
	a := article.Article{
		ID: id, Title: title, Summary: title, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source: source.Source{ID: sid, Type: source.TypeRSS}, ContentType: article.ContentTypeArticle,
		Status: article.StatusPendingPublication, FetchedAt: fetched, PublishedAt: fetched.Add(-time.Hour),
	}
	if err := repo.Save(context.Background(), a); err != nil {
		t.Fatalf("Save(%s): %v", id, err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := infrasqlite.Open(t.TempDir()+"/t.db", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	arts := sqliteadapter.NewArticleRepository(db)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	e := &env{llm: &scriptedLLM{}, repo: repo}
	base := time.Now().Add(-2 * time.Hour)
	for i, title := range []string{"Isotopes sign Random Newcomer", "Isotopes cut Jordan Stranger and Other Newcomer", "Unrelated Outsider Person story"} {
		id := string(rune('a'+i)) + "-id"
		seedArticle(t, arts, id, title, base.Add(time.Duration(i)*time.Minute))
		if i < 2 {
			e.ids = append(e.ids, id)
		}
	}
	e.deps = entitycycle.Deps{
		Lister:    entitycycle.SessionLister{IDs: e.ids, Articles: arts, Processed: repo},
		LLM:       e.llm,
		Proposals: repo,
		Catalog:   entitydiscovery.BuildCatalogIndex(nil),
		Apply: func(p entityproposal.Proposal, _ time.Time) error {
			e.applied = append(e.applied, p.CanonicalName)
			return nil
		},
		Regenerate: func() error { e.regen++; return nil },
		Now:        time.Now,
	}
	return e
}

func run(t *testing.T, e *env, stdin string) (entitycycle.Result, string, error) {
	t.Helper()
	var out bytes.Buffer
	res, err := entitycycle.Run(context.Background(), e.deps, entitycycle.Input{
		SessionArticleIDs: e.ids, ExtractorVersion: "v-test", BatchSize: 5, Limit: 100, VerifyMinConfidence: 0.85,
		Scanner: bufio.NewScanner(strings.NewReader(stdin)), Out: &out,
	})
	return res, out.String(), err
}

func newcomer(name, articleID string) appout.EntityDiscoveryEntity {
	return appout.EntityDiscoveryEntity{Name: name, Type: "player", Confidence: 0.9, SourceArticleIDs: []string{articleID}}
}

func TestRun_ApprovesRejectsAndRegeneratesOnce(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0]), newcomer("Jordan Stranger", e.ids[1])}
	res, out, err := run(t, e, "a\nr\n")
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	if res.Proposed != 2 || res.Approved != 1 || res.Rejected != 1 || res.Paused || res.DiscoveryFailed {
		t.Fatalf("result = %+v\n%s", res, out)
	}
	if len(e.applied) != 1 || e.regen != 1 || !res.Regenerated {
		t.Fatalf("applied=%v regenerated=%d; one approval must apply once and regenerate once", e.applied, e.regen)
	}
}

func TestRun_QuitPausesButStillRegeneratesWhatWasApproved(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0]), newcomer("Jordan Stranger", e.ids[1])}
	res, _, err := run(t, e, "a\nq\n")
	if err != nil || !res.Paused || res.Approved != 1 || e.regen != 1 {
		t.Fatalf("res=%+v err=%v regen=%d; quit pauses and the approval so far is regenerated", res, err, e.regen)
	}
	// Resume: the approved one is no longer pending, the other one is.
	res2, _, err := run(t, e, "r\n")
	if err != nil || res2.Rejected != 1 || res2.Paused || e.llm.calls != 1 {
		t.Fatalf("resume: res=%+v err=%v llm calls=%d; articles already processed must not be re-sent", res2, err, e.llm.calls)
	}
}

func TestRun_EOFPauses(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0])}
	res, _, _ := run(t, e, "")
	if !res.Paused || res.Approved != 0 || e.regen != 0 {
		t.Fatalf("res=%+v regen=%d; EOF is a pause and regenerates nothing", res, e.regen)
	}
}

func TestRun_LLMOutageWarnsAndContinuesWithoutReview(t *testing.T) {
	e := newEnv(t)
	e.llm.err = errors.New("connection refused")
	res, out, err := run(t, e, "a\n")
	if err != nil {
		t.Fatalf("an LLM outage must not fail the step: %v", err)
	}
	if !res.DiscoveryFailed || res.Approved != 0 || !strings.Contains(out, "WARNING") || !strings.Contains(out, "connection refused") {
		t.Fatalf("res=%+v out=%q; want a visible warning naming the cause and no review", res, out)
	}
}

func TestRun_FailedApprovalKeepsTheProposalPending(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0])}
	e.deps.Apply = func(entityproposal.Proposal, time.Time) error { return errors.New("id already exists in the catalog") }
	res, out, _ := run(t, e, "a\ns\n")
	if res.Approved != 0 || res.Skipped != 1 || !strings.Contains(out, "id already exists") {
		t.Fatalf("res=%+v out=%q; a refused approval is reported and the proposal stays pending", res, out)
	}
	pending, _ := e.repo.List(context.Background(), "", "pending")
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
}

func TestRun_OnlyReviewsProposalsOfTheSession(t *testing.T) {
	e := newEnv(t)
	// A pending proposal from some other article must not be offered.
	if _, err := e.repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Unrelated Outsider", NormalizedName: "unrelated outsider", EntityType: entityproposal.EntityTypePlayer,
		Kind: entityproposal.KindNewEntity, Status: entityproposal.StatusPending, Confidence: 0.9, Occurrences: 1,
		Sources: []entityproposal.Source{{ArticleID: "c-id", ArticleTitle: "Unrelated Outsider Person story"}}, FirstSeen: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0])}
	res, out, _ := run(t, e, "a\n")
	if res.Approved != 1 || strings.Contains(out, "Unrelated Outsider") {
		t.Fatalf("res=%+v out=%q; only the session's proposals are offered", res, out)
	}
}

func TestRun_PossibleDuplicateNeedsAnExplicitNewEntityChoice(t *testing.T) {
	e := newEnv(t)
	// Two brand-new candidates sharing a surname are flagged as possible duplicates.
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0]), newcomer("Other Newcomer", e.ids[1])}
	res, out, err := run(t, e, "a\nn\nn\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Approved != 2 || !strings.Contains(out, "possible duplicate") || len(e.applied) != 2 {
		t.Fatalf("res=%+v applied=%v out=%q; a plain 'a' must be refused for a possible duplicate and 'n' must approve it as new", res, e.applied, out)
	}
}

// failingStatusRepo makes UpdateStatus fail after Apply already edited the files.
type failingStatusRepo struct {
	entityproposalport.EntityProposalRepository
}

func (failingStatusRepo) UpdateStatus(context.Context, string, entityproposal.Status, time.Time) error {
	return errors.New("database is locked")
}

func TestRun_ApplyWithoutStatusUpdateStillRegeneratesTheCatalog(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0])}
	e.deps.Proposals = failingStatusRepo{e.repo}
	res, _, err := run(t, e, "a\n")
	if err == nil {
		t.Fatal("a failed status update must surface as an error")
	}
	if len(e.applied) != 1 || e.regen != 1 {
		t.Fatalf("applied=%v regen=%d res=%+v; the files were edited, so the catalog must be regenerated even though the status update failed", e.applied, e.regen, res)
	}
}

func TestRun_AnOutageStillOffersProposalsLeftFromAnEarlierRun(t *testing.T) {
	e := newEnv(t)
	if _, err := e.repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Leftover Rookie", NormalizedName: "leftover rookie", EntityType: entityproposal.EntityTypePlayer,
		Kind: entityproposal.KindNewEntity, Status: entityproposal.StatusPending, Confidence: 0.9, Occurrences: 1,
		Sources: []entityproposal.Source{{ArticleID: e.ids[0], ArticleTitle: "Isotopes sign Random Newcomer"}}, FirstSeen: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	e.llm.err = errors.New("connection refused")
	res, out, err := run(t, e, "a\n")
	if err != nil {
		t.Fatal(err)
	}
	if !res.DiscoveryFailed || res.Approved != 1 || !strings.Contains(out, "Leftover Rookie") {
		t.Fatalf("res=%+v out=%q; pending proposals of this session do not need the LLM and must still be offered", res, out)
	}
}
