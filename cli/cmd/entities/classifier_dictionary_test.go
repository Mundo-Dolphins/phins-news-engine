package entities_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteclassifierproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteclassifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

// seedClassifierProposal stores one pending term proposal in the fixture's
// database and returns its id.
func seedClassifierProposal(t *testing.T, configPath, value string) string {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(configPath), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	id, err := sqliteclassifierproposal.NewClassifierProposalRepository(db).Upsert(context.Background(), classifierproposal.Proposal{
		Kind: classifierproposal.KindTerm, Category: "nfl_people_context", Value: value,
		NormalizedValue: classifierproposal.Normalize(value), Operation: "add",
		Metadata: map[string]string{"origin": "golden"}, Confidence: 0.8, Occurrences: 3,
		Sources: []classifierproposal.Source{{ArticleID: "a1", ArticleTitle: "Title one"}},
		Status:  classifierproposal.StatusPending, FirstSeen: at, LastSeen: at, AnalyzerVersion: "v1", CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	return id
}

func TestClassifierDictionaryListShowMetricsGolden(t *testing.T) {
	configPath, _ := testsupport.WriteFixtureWithDictionary(t)
	id := seedClassifierProposal(t, configPath, "Jai Rene Mossbank")

	out, err := testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "list")
	if err != nil {
		t.Fatalf("list error = %v\n%s", err, out)
	}
	wantList := fmt.Sprintf("ID%s  KIND             CATEGORY            VALUE              OCCURRENCES  CONFIDENCE  STATUS\n%s  classifier_term  nfl_people_context  Jai Rene Mossbank  3            0.80        pending\n",
		strings.Repeat(" ", len(id)-2), id)
	if out != wantList {
		t.Fatalf("list output =\n%q\nwant\n%q", out, wantList)
	}

	out, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "show", id)
	if err != nil {
		t.Fatalf("show error = %v\n%s", err, out)
	}
	wantShow := "Value: Jai Rene Mossbank\nKind: classifier_term\nCategory: nfl_people_context\nOperation: add\n\n" +
		"Occurrences: 3\nConfidence: 0.80\nStatus: pending\nFirst seen: 2026-09-01\nLast seen: 2026-09-01\n\n" +
		"Metadata:\n- origin: golden\n\nEvidence:\n- a1 Title one\n"
	if out != wantShow {
		t.Fatalf("show output =\n%q\nwant\n%q", out, wantShow)
	}

	out, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "metrics", "--since", "87600h")
	if err != nil {
		t.Fatalf("metrics error = %v\n%s", err, out)
	}
	wantMetrics := "catalog_analysis_runs_total=0\ncatalog_proposals_total=1\ncatalog_proposals_approved_total=0\ncatalog_proposals_rejected_total=0\n" +
		"entity_extraction_total=0\nentity_extraction_unknown_total=0\nunknown_entity_count=0\n"
	if out != wantMetrics {
		t.Fatalf("metrics output =\n%q\nwant\n%q", out, wantMetrics)
	}
}

func TestClassifierDictionaryApproveRejectGolden(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)
	approveID := seedClassifierProposal(t, configPath, "Jai Rene Mossbank")
	rejectID := seedClassifierProposal(t, configPath, "Ezo Quarrington")

	out, err := testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "approve", approveID, "--dictionary", dictPath)
	if err != nil {
		t.Fatalf("approve error = %v\n%s", err, out)
	}
	if want := fmt.Sprintf("approved Jai Rene Mossbank (%s)\n", approveID); out != want {
		t.Fatalf("approve output = %q, want %q", out, want)
	}

	out, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "approve", approveID, "--dictionary", dictPath)
	if err == nil {
		t.Fatalf("second approve error = nil, output %q", out)
	}
	if want := fmt.Sprintf("entities classifier-dictionary approve: proposal %s is already approved", approveID); err.Error() != want {
		t.Fatalf("second approve error = %q, want %q", err.Error(), want)
	}

	out, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "reject", rejectID)
	if err != nil {
		t.Fatalf("reject error = %v\n%s", err, out)
	}
	if want := fmt.Sprintf("rejected Ezo Quarrington (%s)\n", rejectID); out != want {
		t.Fatalf("reject output = %q, want %q", out, want)
	}

	_, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "reject", rejectID)
	if err == nil {
		t.Fatal("second reject error = nil")
	}
	if want := fmt.Sprintf("entities classifier-dictionary reject: proposal %s is already rejected", rejectID); err.Error() != want {
		t.Fatalf("second reject error = %q, want %q", err.Error(), want)
	}
}

func TestClassifierDictionaryAnalyzeGolden(t *testing.T) {
	configPath, dictPath := testsupport.WriteFixtureWithDictionary(t)

	out, err := testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "analyze", "--dictionary", dictPath)
	if err != nil {
		t.Fatalf("analyze error = %v\n%s", err, out)
	}
	want := "articles considered: 0\narticle proposals created/updated: 0\nunknown-entity proposals created/updated: 0\n"
	if out != want {
		t.Fatalf("analyze output =\n%q\nwant\n%q", out, want)
	}

	bad := filepath.Join(t.TempDir(), "bad.yml")
	body := `
meta:
  version: "1.0.0"
  name: test_dictionary
  language: en
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 7
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  nfl_people_context:
    weight: 1
    requires_any_of: [missing_category]
    entries: []
`
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatalf("write dictionary: %v", err)
	}
	_, err = testsupport.Execute(t, "--config", configPath, "entities", "classifier-dictionary", "analyze", "--dictionary", bad)
	if err == nil {
		t.Fatal("analyze with an invalid dictionary error = nil")
	}
	wantErr := "entities classifier-dictionary analyze: " + bad + " failed validation: " + `[error] nfl_people_context: requires_any_of references unknown category "missing_category"`
	if err.Error() != wantErr {
		t.Fatalf("analyze error = %q, want %q", err.Error(), wantErr)
	}
}
