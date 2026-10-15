package editorial_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	toolsentities "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/entities"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var baseCycleArgs = []string{"--skip-telegram-import", "--skip-sync", "--skip-deploy", "--skip-train"}

type fakeEntityLLM struct {
	entities []appout.EntityDiscoveryEntity
	err      error
	calls    int
}

func (f *fakeEntityLLM) Discover(context.Context, []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	f.calls++
	if f.err != nil {
		return appout.EntityDiscoveryResult{}, f.err
	}
	return appout.EntityDiscoveryResult{Entities: f.entities}, nil
}

func (f *fakeEntityLLM) VerifyCandidate(context.Context, appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionFinal}, nil
}

func useFakeEntityLLM(t *testing.T, f *fakeEntityLLM) {
	t.Helper()
	old := toolsentities.NewCycleLLM
	toolsentities.NewCycleLLM = func(*cobra.Command, config.EntityDiscoveryConfig, int, bool) appout.EntityDiscoveryLLM { return f }
	t.Cleanup(func() { toolsentities.NewCycleLLM = old })
}

// entityFixture returns a fixture config with the entity step fully configured
// (catalog files in the temp dir, roster pointing at rosterURL).
func entityFixture(t *testing.T, rosterURL string) (configPath, dictPath, generated string) {
	t.Helper()
	configPath, dictPath = testsupport.WriteFixtureWithDictionary(t)
	dir := filepath.Dir(configPath)
	manual := filepath.Join(dir, "manual.yml")
	generated = filepath.Join(dir, "generated.yml")
	const isotopes = "entities:\n    - id: springfield-isotopes\n      canonical_name: Springfield Isotopes\n      kind: team\n      terms:\n        - isotopes\n      is_primary_team: true\n"
	if err := os.WriteFile(manual, []byte(isotopes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("entities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	const off = "clustering:\n  enabled: false\n"
	if !strings.Contains(s, off) {
		t.Fatal("fixture no longer has the clustering block this helper patches")
	}
	s = strings.Replace(s, off, off+fmt.Sprintf("  entity_catalog:\n    manual_path: %q\n    generated_path: %q\n", manual, generated), 1)
	s += fmt.Sprintf(`entity_discovery:
  enabled: true
  extractor_version: entity-discovery-v1
  batch_size: 5
  limit: 50
  llm:
    base_url: http://127.0.0.1:1/v1
    model: test
    temperature: 0
    timeout_seconds: 5
    max_tokens: 512
  wikidata:
    enabled: false
  roster:
    enabled: true
    path: %q
    max_age_days: 0
    url: %q
  league:
    enabled: false
  verification:
    wikidata_first: false
    fallback: false
`, filepath.Join(dir, "roster.csv"), rosterURL)
	if err := os.WriteFile(configPath, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, dictPath, generated
}

func seedTitled(t *testing.T, dbPath, id, title string) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := sqliteadapter.NewArticleRepository(db).Save(context.Background(), article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id, Title: title, Summary: title,
		FetchedAt: now, PublishedAt: now, Status: article.StatusPendingPublication,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatal(err)
	}
}

func readReport(t *testing.T, out string) editorialcycle.AuditReport {
	t.Helper()
	const marker = "audit report: "
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("no audit report line in output:\n%s", out)
	}
	line := out[idx+len(marker):]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	data, err := os.ReadFile(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	var r editorialcycle.AuditReport
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func rosterServer(t *testing.T, status int, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte("full_name,team,position,status,gsis_id,week\nZeno Ashbury,WAV,LB,ACT,g1,5\n"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestEditorialCycleCmd_SkipEntities_NeverTouchesRosterOrLLM(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e1")

	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary", "--skip-entities")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if hits.Load() != 0 || llm.calls != 0 {
		t.Fatalf("roster hits=%d llm calls=%d; --skip-entities must touch neither", hits.Load(), llm.calls)
	}
	r := readReport(t, out)
	if !r.EntitiesSkipped || r.EntitiesSkipReason != "explicit --skip-entities flag" || !strings.HasPrefix(r.RosterSync, "skipped") {
		t.Fatalf("report = %+v", r)
	}
}

func TestEditorialCycleCmd_EntityStepDisabledByConfig_PrintsOneLine(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e2")
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "entities: step disabled (entity_discovery.enabled is false)") {
		t.Fatalf("output lacks the disabled-step line:\n%s", out)
	}
}

func TestEditorialCycleCmd_RosterSyncFailureAbortsNamingTheSkipFlag(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusInternalServerError, &hits))
	useFakeEntityLLM(t, &fakeEntityLLM{})
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e3")
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err == nil || !strings.Contains(err.Error(), "--skip-roster-sync") {
		t.Fatalf("err = %v, want a roster failure naming --skip-roster-sync", err)
	}
	if r := readReport(t, out); r.RosterSync != "failed" || r.SessionID != "" {
		t.Fatalf("report = %+v; the roster failure must precede the session", r)
	}
}

func TestEditorialCycleCmd_SkipRosterSync_WarnsAndRunsTheEntityStep(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusInternalServerError, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e4")
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary", "--skip-roster-sync")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if hits.Load() != 0 || !strings.Contains(out, "--skip-roster-sync") {
		t.Fatalf("hits=%d; want a warning naming the flag and no download\n%s", hits.Load(), out)
	}
	if r := readReport(t, out); r.RosterSync != "skipped:--skip-roster-sync" || llm.calls != 1 {
		t.Fatalf("report=%+v llm calls=%d; the entity step must still run", r, llm.calls)
	}
}

func TestEditorialCycleCmd_DryRun_NeverDownloadsTheRoster(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusInternalServerError, &hits))
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e5")
	args := append([]string{"--config", configPath, "editorial", "cycle", "--dry-run"}, baseCycleArgs...)
	out, err := testsupport.Execute(t, args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if hits.Load() != 0 || !strings.Contains(out, "would synchronise the nflverse roster") {
		t.Fatalf("hits=%d out=%s", hits.Load(), out)
	}
}

func TestEditorialCycleCmd_EntityStep_ApprovesAndRegenerates(t *testing.T) {
	var hits atomic.Int32
	configPath, _, generated := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	seedTitled(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e6", "Isotopes sign Brandon Newcomer")
	llm.entities = []appout.EntityDiscoveryEntity{{
		Name: "Brandon Newcomer", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"e6"},
		Metadata: map[string]string{"team": "Springfield Isotopes", "team_id": "springfield-isotopes", "position": "CB"},
	}}
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\na\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	r := readReport(t, out)
	if hits.Load() != 1 || r.RosterSync != "synced:1" || r.EntitiesApproved != 1 || r.EntitiesProposed != 1 {
		t.Fatalf("hits=%d report=%+v\n%s", hits.Load(), r, out)
	}
	if b, _ := os.ReadFile(generated); !strings.Contains(string(b), "Brandon Newcomer") {
		t.Fatalf("generated catalog = %q; the approved player must be in the regenerated catalog", b)
	}
	if got := proposalsWithStatus(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "approved"); got != 1 {
		t.Fatalf("approved proposals = %d, want 1", got)
	}
}

func proposalsWithStatus(t *testing.T, dbPath, status string) int {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list, err := sqliteentityproposal.NewEntityProposalRepository(db).List(context.Background(), "", status)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestEditorialCycleCmd_EntityStep_RefusedApprovalStaysPending(t *testing.T) {
	var hits atomic.Int32
	configPath, _, generated := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "e8", "Isotopes sign Drew Newcomer")
	// A team that is not catalogued and has no team_id is refused by approval.
	llm.entities = []appout.EntityDiscoveryEntity{{
		Name: "Drew Newcomer", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"e8"},
		Metadata: map[string]string{"team": "Shelbyville Sharks", "position": "WR"},
	}}
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\na\ns\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	r := readReport(t, out)
	if !strings.Contains(out, "Cannot approve Drew Newcomer") || r.EntitiesApproved != 0 {
		t.Fatalf("report=%+v\n%s", r, out)
	}
	if got := proposalsWithStatus(t, dbPath, "pending"); got != 1 {
		t.Fatalf("pending proposals = %d, want the refused one to stay pending", got)
	}
	if b, _ := os.ReadFile(generated); strings.Contains(string(b), "Drew Newcomer") {
		t.Fatalf("a refused approval must not reach the catalog: %q", b)
	}
}

func TestEditorialCycleCmd_NonInteractive_SkipsRosterAndEntityStep(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	args := append([]string{"--config", configPath, "editorial", "cycle", "--non-interactive"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	r := readReport(t, out)
	if hits.Load() != 0 || llm.calls != 0 || !r.EntitiesSkipped || !strings.Contains(r.EntitiesSkipReason, "non-interactive") {
		t.Fatalf("roster hits=%d llm calls=%d report=%+v; an unattended run must not download or call the LLM", hits.Load(), llm.calls, r)
	}
}

func TestEditorialCycleCmd_EntityStep_LLMDown_ContinuesToTheDictionaryStep(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	useFakeEntityLLM(t, &fakeEntityLLM{err: fmt.Errorf("connection refused")})
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e7")
	args := append([]string{"--config", configPath, "editorial", "cycle"}, baseCycleArgs...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err != nil {
		t.Fatalf("an LLM outage must not fail the cycle: %v\n%s", err, out)
	}
	r := readReport(t, out)
	if !strings.Contains(out, "WARNING: entity discovery is unavailable") || !r.EntitiesDiscoveryFailed {
		t.Fatalf("report=%+v\n%s", r, out)
	}
	if !strings.Contains(out, "candidate term") {
		t.Fatalf("the dictionary step must still run after the outage:\n%s", out)
	}
}

func TestEditorialCycleCmd_InvalidEntitiesVerificationIsAUsageError(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	args := append([]string{"--config", configPath, "editorial", "cycle", "--entities-verification", "web"}, baseCycleArgs...)
	if _, err := testsupport.ExecuteWithStdin(t, "", args...); err == nil || !strings.Contains(err.Error(), "entities-verification") {
		t.Fatalf("err = %v, want a usage error naming --entities-verification", err)
	}
}

// Quitting inside the entity review leaves the proposals pending in the
// database, but the session is already completed, so --resume cannot re-enter
// the step. The message must point at the command that can finish the review.
func TestEditorialCycleCmd_EntityStep_QuitTellsHowToFinishTheReview(t *testing.T) {
	var hits atomic.Int32
	configPath, _, _ := entityFixture(t, rosterServer(t, http.StatusOK, &hits))
	llm := &fakeEntityLLM{}
	useFakeEntityLLM(t, llm)
	seedTitled(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "e9", "Isotopes sign Casey Newcomer")
	llm.entities = []appout.EntityDiscoveryEntity{{Name: "Casey Newcomer", Type: "player", Confidence: 0.9, SourceArticleIDs: []string{"e9"}}}
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\nq\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "entities review") || strings.Contains(out, "resume with --resume") {
		t.Fatalf("the pause message must point at `entities review` and not promise --resume:\n%s", out)
	}
}
