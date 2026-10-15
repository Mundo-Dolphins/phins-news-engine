package train_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// snapshotLabelTables dumps every row of human_labels and
// editorial_examples in a stable order, so a before/after comparison
// around a recheck-corpus run proves the command wrote nothing to either
// table -- not just that its stdout looked right. It is intentionally a
// plain string, not a row count: a row count alone could miss a write that
// updated an existing row (e.g. flipped active, bumped revision) without
// changing the total number of rows.
func snapshotLabelTables(t *testing.T, db *sql.DB) string {
	t.Helper()
	var sb strings.Builder

	humanLabelsRows, err := db.Query(`
		SELECT article_id, label, reason, labeled_by, revision, active, supersedes, content_hash, source
		FROM human_labels ORDER BY article_id, revision
	`)
	if err != nil {
		t.Fatalf("query human_labels: %v", err)
	}
	defer humanLabelsRows.Close()
	for humanLabelsRows.Next() {
		var articleID, label, reason, labeledBy, contentHash, source string
		var revision, active int
		var supersedes sql.NullInt64
		if err := humanLabelsRows.Scan(&articleID, &label, &reason, &labeledBy, &revision, &active, &supersedes, &contentHash, &source); err != nil {
			t.Fatalf("scan human_labels row: %v", err)
		}
		fmt.Fprintf(&sb, "human_labels:%s|%s|%s|%s|%d|%d|%v|%s|%s\n",
			articleID, label, reason, labeledBy, revision, active, supersedes, contentHash, source)
	}
	if err := humanLabelsRows.Err(); err != nil {
		t.Fatalf("iterate human_labels rows: %v", err)
	}

	editorialExamplesRows, err := db.Query(`
		SELECT article_id, label, reason, title, body, revision, active, supersedes, content_hash, source
		FROM editorial_examples ORDER BY article_id, revision
	`)
	if err != nil {
		t.Fatalf("query editorial_examples: %v", err)
	}
	defer editorialExamplesRows.Close()
	for editorialExamplesRows.Next() {
		var articleID, label, reason, title, body, contentHash, source string
		var revision, active int
		var supersedes sql.NullInt64
		if err := editorialExamplesRows.Scan(&articleID, &label, &reason, &title, &body, &revision, &active, &supersedes, &contentHash, &source); err != nil {
			t.Fatalf("scan editorial_examples row: %v", err)
		}
		fmt.Fprintf(&sb, "editorial_examples:%s|%s|%s|%s|%s|%d|%d|%v|%s|%s\n",
			articleID, label, reason, title, body, revision, active, supersedes, contentHash, source)
	}
	if err := editorialExamplesRows.Err(); err != nil {
		t.Fatalf("iterate editorial_examples rows: %v", err)
	}

	return sb.String()
}

// TestRecheckCorpusCmd_FlagsExampleThatWouldNowBeRejected exercises the
// read-only recheck-corpus report end to end against the test dictionary
// (WriteFixtureWithDictionary's testDictionaryYAML, which scores "Isotopes"
// in the official_names category at weight 10 against a
// positive_threshold_high of 10 -- easily cleared once the title weight
// multiplier is applied). One seeded article's title unambiguously
// mentions the Isotopes and is labeled publish -- recheck-corpus must NOT
// flag it. A second seeded article's title has nothing dictionary-related
// in it at all and is also labeled publish -- recheck-corpus must flag it,
// since the current dictionary would now score it Related=false.
func TestRecheckCorpusCmd_FlagsExampleThatWouldNowBeRejected(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	if err := articles.Save(context.Background(), article.Article{
		ID: "isotopes-article", Title: "Springfield Isotopes sign new linebacker",
		Status: article.StatusFetched, FetchedAt: time.Now().UTC(),
		Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed isotopes article: %v", err)
	}
	if err := articles.Save(context.Background(), article.Article{
		ID: "unrelated-article", Title: "Local weather forecast for the weekend",
		Status: article.StatusFetched, FetchedAt: time.Now().UTC(),
		Source: testSource(t),
	}); err != nil {
		db.Close()
		t.Fatalf("seed unrelated article: %v", err)
	}
	db.Close()

	if _, err := testsupport.Execute(t, "--config", configPath, "label", "set", "isotopes-article", "publish"); err != nil {
		t.Fatalf("seed isotopes-article label Execute() error = %v", err)
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "label", "set", "unrelated-article", "publish"); err != nil {
		t.Fatalf("seed unrelated-article label Execute() error = %v", err)
	}

	snapshotDB, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db for before-snapshot: %v", err)
	}
	before := snapshotLabelTables(t, snapshotDB)
	snapshotDB.Close()
	if before == "" {
		t.Fatal("before-snapshot of human_labels/editorial_examples is empty -- test setup didn't seed what it expects, the read-only comparison below would be vacuous")
	}

	out, err := testsupport.Execute(t, "--config", configPath, "train", "recheck-corpus")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	snapshotDB, err = infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db for after-snapshot: %v", err)
	}
	after := snapshotLabelTables(t, snapshotDB)
	snapshotDB.Close()
	if before != after {
		t.Errorf("recheck-corpus wrote to human_labels/editorial_examples -- it must be read-only\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(out, "examined") {
		t.Errorf("output = %q, want it to report how many examples were examined", out)
	}
	if !strings.Contains(out, "2 publish-labeled example(s) examined") {
		t.Errorf("output = %q, want it to report 2 publish-labeled example(s) examined", out)
	}
	if strings.Contains(out, "FLAGGED isotopes-article") {
		t.Errorf("output = %q, want isotopes-article (unambiguously Isotopes-related) NOT flagged", out)
	}
	if !strings.Contains(out, "FLAGGED unrelated-article") {
		t.Errorf("output = %q, want unrelated-article (no dictionary match) flagged", out)
	}
}
