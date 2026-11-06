package datasetaudit_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func testMatcher(t *testing.T) dictionary.Matcher {
	t.Helper()
	m, err := dictionary.Prepare(dictionary.Dictionary{
		Version: "t",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Confidence: 1}}},
			{Name: "venue", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Isotopes Park", Confidence: 1}}},
			{Name: "roster", Weight: 8, Entries: []dictionary.Entry{{CanonicalName: "jones", Confidence: 0.5}, {CanonicalName: "Bart Simpson", Confidence: 0.9}}},
		},
		Decision: dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testGateAudit(t *testing.T) datasetaudit.GateAuditor {
	t.Helper()
	return datasetaudit.GateAuditor{
		Matcher: testMatcher(t), Text: normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3},
		Anchor: classification.AnchorPolicy{
			Enabled: true, MinConfidence: 0.7,
			Categories:          map[string]bool{"official_names": true, "roster": true},
			MultiWordCategories: map[string]bool{"roster": true},
		},
		Trusted: map[string]bool{"web:official": true}, Context: map[string]bool{}, Exempt: map[string]bool{},
		Veto:      &editorialveto.Config{Rules: []editorialveto.Rule{{Category: "gambling", ReasonCode: "veto:gambling", Patterns: []string{"prop bets"}}}},
		Evaluated: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
}

func TestAuditGatesFlagsOnlyPublishRowsTheGatesWouldBlock(t *testing.T) {
	g := testGateAudit(t)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "ok", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes win again"},
		{ArticleID: "venue-only", Label: "publish", SourceID: "rss:x", Title: "Weather at Isotopes Park tonight"},
		{ArticleID: "surname", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes lose", Body: ""},
		{ArticleID: "veto", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes prop bets"},
		{ArticleID: "weak-name", Label: "publish", SourceID: "rss:x", Title: "Jones was great and Bart Simpson too"},
		{ArticleID: "trusted", Label: "publish", SourceID: "web:official", Title: "Weather at Isotopes Park tonight"},
		{ArticleID: "already-reject", Label: "reject", SourceID: "rss:x", Title: "Weather at Isotopes Park tonight"},
	}
	got := map[string]string{}
	for _, c := range g.Audit(examples) {
		got[c.ArticleID] = c.Reason
	}
	for _, id := range []string{"ok", "trusted", "already-reject", "surname", "weak-name"} {
		if _, bad := got[id]; bad {
			t.Errorf("%s must not be flagged, got %q", id, got[id])
		}
	}
	if r := got["veto"]; r == "" || r[:5] != "veto:" {
		t.Errorf("veto row reason = %q, want veto: prefix", r)
	}
	if got["venue-only"] == "" {
		t.Error("venue-only row must be flagged as having no anchor")
	}
}

func TestAuditDatasetFlagsHeuristicRows(t *testing.T) {
	got := map[string]string{}
	for _, c := range datasetaudit.AuditDataset([]editorialtrain.TrainingExample{
		{ArticleID: "p1", Title: "Same title", Label: "publish", LabelSource: "cli:label"},
		{ArticleID: "r1", Title: "Same title!", Label: "reject", LabelSource: "cli:label"},
		{ArticleID: "p2", Title: "Short post", Label: "publish", LabelSource: "telegramimport"},
		{ArticleID: "r2", Title: "Weak reject", Label: "reject", LabelSource: "pipeline_backfill"},
		{ArticleID: "fine", Title: "A perfectly normal long enough title for the dataset", Label: "publish", LabelSource: "cli:label"},
	}) {
		got[c.ArticleID] = c.Reason
	}
	for _, id := range []string{"p1", "r1", "p2", "r2"} {
		if got[id] == "" {
			t.Errorf("%s not flagged", id)
		}
	}
	if _, bad := got["fine"]; bad {
		t.Error("fine must not be flagged")
	}
}

func TestWriteCSVWritesHeaderAndRowsPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "out.csv")
	if err := datasetaudit.WriteCSV(path, []datasetaudit.Candidate{{ArticleID: "a", Reason: "why, really"}}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) != 2 || rows[0][0] != "article_id" || rows[1][1] != "why, really" {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestRecheckCorpusSkipsRejects(t *testing.T) {
	m := testMatcher(t)
	examined, flagged := datasetaudit.RecheckCorpus([]training.EditorialExample{
		{ArticleID: "good", Label: "publish", Title: "Springfield Isotopes win again"},
		{ArticleID: "stale", Label: "publish", Title: "Weather report for Tuesday"},
		{ArticleID: "rej", Label: "reject", Title: "Weather report for Tuesday"},
	}, m)
	if examined != 2 {
		t.Errorf("examined = %d, want 2 (rejects skipped)", examined)
	}
	if len(flagged) != 1 || flagged[0].ArticleID != "stale" || flagged[0].Title != "Weather report for Tuesday" || flagged[0].Rule == "" {
		t.Errorf("flagged = %+v", flagged)
	}
}
