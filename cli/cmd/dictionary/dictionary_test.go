package dictionary_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const validDictionaryYAML = `
meta:
  version: "1.0.0"
  name: test_dict
  language: en
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  official_names:
    weight: 12
    strong_override: true
    entries:
      - canonical_name: "Springfield Isotopes"
        source: manual
        confidence: 1.0
`

const invalidDictionaryYAML = `
meta:
  version: "1.0.0"
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  a:
    weight: 1
    entries:
      - canonical_name: "isotopes"
        source: x
  b:
    weight: 1
    entries:
      - canonical_name: "Isotopes"
        source: y
`

func writeDictionaryFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dictionary.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDictionaryValidate_ValidFileSucceeds(t *testing.T) {
	path := writeDictionaryFixture(t, validDictionaryYAML)

	out, err := testsupport.Execute(t, "dictionary", "validate", path)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, out)
	}
	if !strings.Contains(out, "dictionary OK") {
		t.Errorf("output = %q, want it to contain %q", out, "dictionary OK")
	}
}

func TestDictionaryValidate_InvalidFileFailsAndListsIssues(t *testing.T) {
	path := writeDictionaryFixture(t, invalidDictionaryYAML)

	out, err := testsupport.Execute(t, "dictionary", "validate", path)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for a dictionary with a duplicate canonical name")
	}
	if !strings.Contains(out, "[error]") {
		t.Errorf("output = %q, want it to contain an [error] issue line", out)
	}
}

func TestDictionaryValidate_MissingFile(t *testing.T) {
	_, err := testsupport.Execute(t, "dictionary", "validate", filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want error for a missing file")
	}
}

func TestDictionaryInspect_Success(t *testing.T) {
	path := writeDictionaryFixture(t, validDictionaryYAML)

	out, err := testsupport.Execute(t, "dictionary", "inspect", path)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "version: 1.0.0") {
		t.Errorf("output = %q, want it to contain %q", out, "version: 1.0.0")
	}
	if !strings.Contains(out, "categories: 1") {
		t.Errorf("output = %q, want it to contain %q", out, "categories: 1")
	}
	if !strings.Contains(out, "total entries: 1") {
		t.Errorf("output = %q, want it to contain %q", out, "total entries: 1")
	}
}

func seedCompletedSessionWithPublishedArticle(t *testing.T, dbPath string) (sessionID, articleID string) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	articleID = "a1"
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Title: "Ezo Quarrington leads training camp session", FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
		Source: source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-feed"), Name: "Test Feed", Type: source.TypeRSS, Domain: "example.com", URL: "https://example.com"},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	sessionID = "sess-1"
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	if err := sessions.Create(ctx, out.EditorialCycleSession{ID: sessionID, StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	if err := reviewDecisions.Save(ctx, out.ReviewDecision{SessionID: sessionID, ArticleID: articleID, Action: "publish", Label: "publish", DecidedAt: now}); err != nil {
		t.Fatalf("seed review decision: %v", err)
	}
	labels := sqliteadapter.NewHumanLabelRepository(db)
	if err := labels.Label(ctx, training.Example{ArticleID: articleID, Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("seed human label: %v", err)
	}
	return sessionID, articleID
}

func TestDictionaryUpdate_DryRun_ListsCandidatesWithoutMutating(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	out, err := testsupport.Execute(t, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID, "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "Ezo Quarrington") {
		t.Errorf("output = %q, want it to mention the mined candidate", out)
	}

	before, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(before), `version: "1.0.0"`) {
		t.Errorf("dictionary file was modified by --dry-run: %s", before)
	}
}

func TestDictionaryUpdate_ApproveCandidate_WritesEntryAndBumpsVersion(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// "a" approves the first (highest-support) candidate into its guessed
	// category, "q" quits the loop for every remaining candidate.
	out, err := testsupport.ExecuteWithStdin(t, "a\nq\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "1.0.1") {
		t.Errorf("output = %q, want it to mention the new version 1.0.1", out)
	}

	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(after), "Ezo Quarrington") {
		t.Errorf("dictionary file = %s, want the approved entry written", after)
	}
	if !strings.Contains(string(after), `version: 1.0.1`) && !strings.Contains(string(after), `version: "1.0.1"`) {
		t.Errorf("dictionary file = %s, want version bumped to 1.0.1", after)
	}
}

// TestDictionaryUpdate_ApproveCandidate_SchemaV2_RecomputesChecksum guards
// against a regression where writeDictionaryIfApproved appended the
// approved entry and bumped the version but left the on-disk schema-v2
// meta.checksum stamped for the pre-append content, so the post-write
// Validate call always failed with a checksum mismatch on any
// already-schema-v2 dictionary (e.g. one previously written by `dictionary
// update`'s own approval workflow, which does stamp a checksum).
func TestDictionaryUpdate_ApproveCandidate_SchemaV2_RecomputesChecksum(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	before, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("load fixture dictionary: %v", err)
	}
	before.SchemaVersion = 2
	before.Checksum = dictionary.ContentHash(before)
	if err := dictionaryfile.Save(dictPath, before); err != nil {
		t.Fatalf("save schema-v2 fixture dictionary: %v", err)
	}

	outStr, err := testsupport.ExecuteWithStdin(t, "a\nq\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, outStr)
	}

	after, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("load written dictionary: %v", err)
	}
	if want := dictionary.ContentHash(after); after.Checksum != want {
		t.Errorf("dictionary.Checksum = %q, want recomputed hash %q", after.Checksum, want)
	}
	for _, issue := range dictionary.Validate(after) {
		if issue.Severity == dictionary.SeverityError {
			t.Errorf("Validate(after) issue = %q, want no error-severity issues", issue.String())
		}
	}
}

func TestDictionaryUpdate_NoSessionGiven_UsesLatestCompleted(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	out, err := testsupport.Execute(t, "--config", configPath, "dictionary", "mine-candidates", "--dry-run")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, sessionID) {
		t.Errorf("output = %q, want it to mention the auto-selected session %q", out, sessionID)
	}
}

func TestDictionaryUpdate_NoCompletedSession_Errors(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)

	_, err := testsupport.Execute(t, "--config", configPath, "dictionary", "mine-candidates", "--dry-run")
	if err == nil {
		t.Error("Execute() error = nil, want an error when no completed session exists and --session wasn't given")
	}
}

func TestDictionaryUpdate_RejectCandidate_DoesNotWriteDictionary(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// Reject every mined candidate.
	stdin := strings.Repeat("r\n", 20)
	out, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "no candidates approved") {
		t.Errorf("output = %q, want it to report that nothing was approved", out)
	}

	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(after), `version: "1.0.0"`) {
		t.Errorf("dictionary file = %s, want version unchanged after an all-reject pass", after)
	}
}

func TestDictionaryUpdate_CategorizeCandidate_PromptsForCategoryAndWrites(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// "c" categorizes the first candidate, prompting for a category name;
	// give it "coaching_staff", then "q" for everything remaining.
	stdin := "c\ncoaching_staff\nq\n"
	outStr, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(outStr, "category [number or name]:") {
		t.Errorf("output = %q, want it to prompt for a category name", outStr)
	}

	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(after), "coaching_staff") {
		t.Errorf("dictionary file = %s, want the entry written under coaching_staff", after)
	}
}

func TestDictionaryUpdate_UnknownAction_ReportsAndContinues(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// An unrecognized action, then "q" to quit for the rest.
	stdin := "z\n" + strings.Repeat("q\n", 20)
	outStr, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(outStr, `unknown action "z"`) {
		t.Errorf("output = %q, want it to report the unknown action", outStr)
	}
}

func TestDictionaryUpdate_UnknownCategory_ReportsAndReprompts(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// "c" categorizes, gives an unknown category first, then a valid one.
	stdin := "c\nnope_not_real\ncoaching_staff\nq\n"
	outStr, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(outStr, `unknown category "nope_not_real"`) {
		t.Errorf("output = %q, want it to report the unknown category", outStr)
	}

	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(after), "coaching_staff") {
		t.Errorf("dictionary file = %s, want the entry eventually written under coaching_staff", after)
	}
}

func TestDictionaryUpdate_ResumeSession_SkipsAlreadyDecidedCandidates(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// First run: approve the first candidate, quit for the rest.
	if _, err := testsupport.ExecuteWithStdin(t, "a\nq\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	after1, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary after first run: %v", err)
	}

	// Second run against the same session: mining re-runs against the
	// same articles (candidates persisted already are left untouched by
	// Save's ON CONFLICT DO NOTHING), and the approval loop should skip
	// the already-decided (approved) candidate, only presenting whatever
	// is still pending. Reject everything remaining.
	stdin := strings.Repeat("r\n", 20)
	outStr, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if strings.Contains(outStr, "Term:      Ezo Quarrington") {
		t.Errorf("output = %q, want the already-approved candidate not re-presented", outStr)
	}

	after2, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary after second run: %v", err)
	}
	if string(after1) != string(after2) {
		t.Errorf("dictionary file changed on an all-reject resume pass:\nbefore=%s\nafter=%s", after1, after2)
	}
}

func TestDictionaryUpdate_ApproveWithUnknownCategoryGuess_FallsThroughToPrompt(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// The highest-support candidate ("Ezo Quarrington") is guessed into
	// nfl_people_context, which exists in the fixture dictionary, so "a"
	// on it alone would never exercise promptForCategory. Reject it, then
	// approve the next candidate — one of the generic single-token/bigram
	// terms mined with no category guess ("" is never a real category
	// name) — which must fall through to the category prompt.
	stdin := "r\na\ncoaching_staff\nq\n"
	outStr, err := testsupport.ExecuteWithStdin(t, stdin, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(outStr, "category [number or name]:") {
		t.Errorf("output = %q, want [a]pprove on a candidate with no/unknown category guess to prompt for a category", outStr)
	}

	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if !strings.Contains(string(after), "coaching_staff") {
		t.Errorf("dictionary file = %s, want the approved entry written under coaching_staff", after)
	}
}

func TestDictionaryUpdate_CategoryPromptEOF_ReturnsDistinctError(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// "c" categorizes the first candidate but stdin ends before a category
	// name is given — promptForCategory must return its own "no category
	// provided before input ended" error, not a bare %!w(<nil>)-style
	// wrapping of a nil scanner.Err().
	outStr, err := testsupport.ExecuteWithStdin(t, "c\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when stdin ends before a category is given")
	}
	if !strings.Contains(err.Error(), "no category provided before input ended") {
		t.Errorf("Execute() error = %v (output %q), want it to mention input ending before a category was given", err, outStr)
	}
}

// flakyReader succeeds for a fixed sequence of lines and then fails with a
// genuine (non-EOF) read error — used to distinguish "stdin ended cleanly"
// from "a real I/O error occurred while reading stdin" in promptForCategory
// and the main approval loop.
type flakyReader struct {
	lines []string
	idx   int
}

func (r *flakyReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.lines) {
		return 0, fmt.Errorf("simulated stdin read failure")
	}
	line := r.lines[r.idx]
	r.idx++
	n := copy(p, []byte(line))
	return n, nil
}

func TestDictionaryUpdate_MainLoopReadError_IsWrapped(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	_, err := testsupport.ExecuteWithReader(t, &flakyReader{}, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped stdin read error")
	}
	if !strings.Contains(err.Error(), "dictionary update: read input") {
		t.Errorf("Execute() error = %v, want it to mention a wrapped read-input error", err)
	}
}

func TestDictionaryUpdate_CategoryPromptReadError_IsWrapped(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)

	// "c" is read successfully, then the reader fails for real (not EOF)
	// while promptForCategory is waiting on the category name.
	_, err := testsupport.ExecuteWithReader(t, &flakyReader{lines: []string{"c\n"}}, "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err == nil {
		t.Fatal("Execute() error = nil, want a wrapped category-input read error")
	}
	if !strings.Contains(err.Error(), "dictionary update: read category input") {
		t.Errorf("Execute() error = %v, want it to mention a wrapped read-category-input error", err)
	}
}

func TestDictionaryUpdate_NumberedCategorySelection(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, _ := seedCompletedSessionWithPublishedArticle(t, dbPath)
	dict, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	output, err := testsupport.ExecuteWithStdin(t, "c\n0\n999\n1\nq\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for i, category := range dict.Categories {
		if !strings.Contains(output, fmt.Sprintf("%d. %s", i+1, category.Name)) {
			t.Fatalf("category %s missing from menu: %s", category.Name, output)
		}
	}
	if !strings.Contains(output, `unknown category "0"`) || !strings.Contains(output, `unknown category "999"`) {
		t.Fatalf("invalid category numbers did not retry: %s", output)
	}
	updated, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range updated.Categories[0].Entries {
		if entry.CanonicalName == "Ezo Quarrington" {
			return
		}
	}
	t.Fatalf("number 1 did not write Ezo Quarrington into %s", updated.Categories[0].Name)
}

func TestDictionaryUpdate_RejectionsDisappearAcrossSessionsAndCaseVariants(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	firstSession, articleID := seedCompletedSessionWithPublishedArticle(t, dbPath)
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE articles SET title = 'Keep Going. Keep Going. Keep Going. Corwin Pellow.' WHERE id = ?`, articleID); err != nil {
		t.Fatal(err)
	}
	// Reject the highest-support phrase, then save the remaining candidates.
	if _, err := testsupport.ExecuteWithStdin(t, "r\nq\n", "--config", configPath, "dictionary", "mine-candidates", "--session", firstSession); err != nil {
		t.Fatal(err)
	}
	repo := sqliteadapter.NewDictionaryCandidateRepository(db)
	rejected, err := repo.ListRejectedTerms(context.Background())
	if err != nil || len(rejected) != 1 || rejected[0] != "Keep Going" {
		t.Fatalf("rejections = %v, error = %v, want Keep Going", rejected, err)
	}
	secondSession := "sess-2"
	now := time.Now()
	if err := sqliteadapter.NewEditorialCycleSessionRepository(db).Create(context.Background(), out.EditorialCycleSession{ID: secondSession, StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.NewReviewDecisionRepository(db).Save(context.Background(), out.ReviewDecision{SessionID: secondSession, ArticleID: articleID, Action: "publish", Label: "publish", DecidedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Model a lowercase variant that was already pending before the rejection.
	if err := repo.Save(context.Background(), out.DictionaryCandidate{Term: "keep going", SessionID: secondSession, SupportCount: 10, FirstSeenArticle: articleID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{firstSession, secondSession} {
		for _, dryRun := range []bool{true, false} {
			args := []string{"--config", configPath, "dictionary", "mine-candidates", "--session", sessionID}
			if dryRun {
				args = append(args, "--dry-run")
			}
			output, err := testsupport.ExecuteWithStdin(t, "q\n", args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(output), "keep going") {
				t.Fatalf("rejected phrase resurfaced for %s (dry run %t): %s", sessionID, dryRun, output)
			}
			if !strings.Contains(output, "Corwin Pellow") {
				t.Fatalf("legitimate new name disappeared: %s", output)
			}
		}
	}
}

func TestDictionaryUpdate_UpdatedPolicyHidesPreviouslyPendingNoise(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, articleID := seedCompletedSessionWithPublishedArticle(t, dbPath)
	if _, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID); err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteadapter.NewDictionaryCandidateRepository(db).Save(context.Background(), out.DictionaryCandidate{Term: "Keep Going", SessionID: sessionID, SupportCount: 100, FirstSeenArticle: articleID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(filepath.Dir(configPath), "dictionary_mining.yml")
	if err := os.WriteFile(policyPath, []byte("min_support: 1\nignored_tokens: [going]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := testsupport.ExecuteWithStdin(t, "q\n", "--config", configPath, "dictionary", "mine-candidates", "--session", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "Keep Going") || !strings.Contains(output, "Ezo Quarrington") {
		t.Fatalf("incorrect candidates after policy update: %s", output)
	}
}

func TestDictionaryUpdate_CatalogExclusionsApplyToMiningAndResumedCandidates(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	sessionID, articleID := seedCompletedSessionWithPublishedArticle(t, dbPath)
	manualPath := filepath.Join(filepath.Dir(configPath), "manual.yml")
	generatedPath := filepath.Join(filepath.Dir(configPath), "generated.yml")
	if err := os.WriteFile(manualPath, []byte("entities:\n  - id: ashcombe\n    canonical_name: Da'Rel Ashcombe\n    kind: player\n    terms: [Ashcombe]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generatedPath, []byte("entities:\n  - id: ezo\n    canonical_name: Ezo Quarrington\n    kind: player\n    terms: [Ezo, Quarrington]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := strings.Replace(string(configBytes), "clustering:\n", fmt.Sprintf("clustering:\n  entity_catalog:\n    manual_path: %s\n    generated_path: %s\n", manualPath, generatedPath), 1)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	a, err := articles.FindByID(context.Background(), articleID)
	if err != nil {
		t.Fatal(err)
	}
	a.Title = "Da’Rel Ashcombe meets Ezo Quarrington and D'Ambry Thistlecombe"
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	repo := sqliteadapter.NewDictionaryCandidateRepository(db)
	for _, term := range []string{"Rel Ashcombe", "ashcombe", "Ezo Quarrington"} {
		if err := repo.Save(context.Background(), out.DictionaryCandidate{Term: term, SessionID: sessionID, SupportCount: 100, FirstSeenArticle: articleID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--config", configPath, "dictionary", "mine-candidates", "--session", sessionID, "--dry-run"},
		{"--config", configPath, "dictionary", "mine-candidates", "--session", sessionID},
	} {
		output, err := testsupport.ExecuteWithStdin(t, "q\n", args...)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(output)
		if strings.Contains(lower, "ashcombe") || strings.Contains(lower, "quarrington") || strings.Contains(lower, "rel") {
			t.Fatalf("catalog name or old fragment offered for review: %s", output)
		}
		if !strings.Contains(output, "D'Ambry Thistlecombe") {
			t.Fatalf("unknown punctuated name missing: %s", output)
		}
	}
	after, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dictionary changed without approvals")
	}
}
