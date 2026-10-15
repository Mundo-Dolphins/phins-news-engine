package label_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestLabelSetCmd_SavesHumanLabel(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	out, err := testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "publish", "--by", "tester", "--notes", "looks good")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, articleID) {
		t.Errorf("output = %q, want it to mention %q", out, articleID)
	}
}

func TestLabelSetCmd_RejectsInvalidLabel(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	if _, err := testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "maybe"); err == nil {
		t.Error("Execute() error = nil, want an error for label \"maybe\" (only publish|reject are valid)")
	}
}

func TestLabelSetCmd_SecondCallRelabelsInsteadOfDuplicating(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	if _, err := testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "publish"); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	out, err := testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "reject")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if !strings.Contains(out, "relabeled") {
		t.Errorf("output = %q, want it to say \"relabeled\"", out)
	}

	historyOut, err := testsupport.Execute(t, "--config", configPath, "label", "history", articleID)
	if err != nil {
		t.Fatalf("history Execute() error = %v", err)
	}
	if !strings.Contains(historyOut, "rev 1") || !strings.Contains(historyOut, "rev 2") {
		t.Errorf("history output = %q, want both rev 1 and rev 2 listed", historyOut)
	}
}

func TestLabelSetCmd_RefusesArticleWithUnresolvedConflict(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO human_label_conflicts (table_name, article_id, detail) VALUES ('human_labels', ?, 'seeded for test')`, articleID,
	); err != nil {
		db.Close()
		t.Fatalf("seed conflict: %v", err)
	}
	db.Close()

	_, err = testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "publish")
	if err == nil {
		t.Fatal("Execute() error = nil, want a refusal: labeling straight through would orphan the unresolved conflict row")
	}
	if !strings.Contains(err.Error(), "resolve-conflict") {
		t.Errorf("error = %q, want it to point the operator at \"resolve-conflict\"", err)
	}
}

// TestLabelSetCmd_ResolvesHardExampleCandidate covers the whole-branch
// review's Finding 2: HardExampleRepository.MarkResolved had no
// production call site, so retraintrigger's MinHardExampleCount --
// sourced from UnresolvedCount -- could only ever grow and would latch
// on forever once an operator enabled it. Applying a human label is
// exactly the event migration 0039 says resolves a candidate.
func TestLabelSetCmd_ResolvesHardExampleCandidate(t *testing.T) {
	configPath, articleID := testsupport.SeedArticleForLabeling(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo := sqliteadapter.NewHardExampleRepository(db)
	if err := repo.Upsert(context.Background(), training.HardExampleCandidate{
		ArticleID: articleID, Kind: "positive", NBScore: 5, LLMLabel: "publish", LLMConfidence: 0.95,
		MinedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		db.Close()
		t.Fatalf("seed hard example candidate: %v", err)
	}
	db.Close()

	if _, err := testsupport.Execute(t, "--config", configPath, "label", "set", articleID, "publish"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	db, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()
	count, err := sqliteadapter.NewHardExampleRepository(db).UnresolvedCount(context.Background())
	if err != nil {
		t.Fatalf("UnresolvedCount() error = %v", err)
	}
	if count != 0 {
		t.Errorf("UnresolvedCount() = %d, want 0 (labeling the article must mark its candidate resolved)", count)
	}
}

func TestLabelConflictsCmd_ReportsNoneOnCleanDB(t *testing.T) {
	configPath, _ := testsupport.SeedArticleForLabeling(t)
	out, err := testsupport.Execute(t, "--config", configPath, "label", "conflicts")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "no unresolved") {
		t.Errorf("output = %q, want it to report no conflicts", out)
	}
}
