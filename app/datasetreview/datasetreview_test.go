package datasetreview_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetreview"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T) datasetreview.Paths {
	t.Helper()
	dir := t.TempDir()
	p := datasetreview.Paths{
		Dataset: filepath.Join(dir, "dataset.yml"),
		Audit:   filepath.Join(dir, "audit.csv"),
		Out:     filepath.Join(dir, "reviewed.yml"),
	}
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", Title: "One", Body: "B1", SourceID: "rss:test", Label: "reject", LabelSource: "cli:label", Revision: 1},
		{ArticleID: "a2", Title: "Two", Body: "B2", SourceID: "rss:test", Label: "publish", LabelSource: "cli:label", Revision: 2},
		{ArticleID: "a3", Title: "Three", Body: "B3", SourceID: "rss:excluded", Label: "publish", LabelSource: "cli:label", Revision: 1},
	}
	if err := editorialtrain.SaveDataset(p.Dataset, examples); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Audit, []byte("article_id,audit_reason\na1,r1\na2,r2\na1,dup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func open(t *testing.T, p datasetreview.Paths) *datasetreview.Session {
	t.Helper()
	s, err := datasetreview.Open(p, map[string]bool{"rss:excluded": true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

func TestPaths(t *testing.T) {
	p := datasetreview.Paths{Out: "x.yml"}
	if p.Log() != "x.yml.review.csv" {
		t.Errorf("Log() = %q", p.Log())
	}
}

func TestOpenFreshExcludesSourcesAndDedupesAudit(t *testing.T) {
	s := open(t, fixture(t))
	if len(s.Examples) != 2 || len(s.Candidates) != 2 {
		t.Fatalf("examples=%d candidates=%d, want 2 and 2", len(s.Examples), len(s.Candidates))
	}
}

func TestOpenResumesMatchingCopy(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	entry, err := s.Apply("a1", datasetreview.Publish, "rev", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	again := open(t, p)
	if again.Examples[0].Label != "publish" || again.Examples[0].LabelSource != "interactive_review" {
		t.Errorf("resumed example = %+v, want the saved decision", again.Examples[0])
	}
	if !again.Logged["a1"] {
		t.Error("a1 should be logged after resume")
	}
}

func TestOpenRejectsMismatchedCopy(t *testing.T) {
	p := fixture(t)
	if err := editorialtrain.SaveDataset(p.Out, []editorialtrain.TrainingExample{{ArticleID: "a1", Title: "One", Label: "reject"}}); err != nil {
		t.Fatal(err)
	}
	_, err := datasetreview.Open(p, nil)
	if err == nil || !strings.Contains(err.Error(), "use a new --out path") {
		t.Fatalf("Open() error = %v, want mismatch with CLI message", err)
	}
	if !errors.Is(err, datasetreview.ErrResumeMismatch) {
		t.Errorf("error should match ErrResumeMismatch: %v", err)
	}
}

func TestOpenRejectsSamePath(t *testing.T) {
	p := fixture(t)
	p.Out = p.Dataset
	_, err := datasetreview.Open(p, nil)
	if !errors.Is(err, datasetreview.ErrSamePath) {
		t.Fatalf("Open() error = %v, want ErrSamePath", err)
	}
}

func TestOpenErrors(t *testing.T) {
	p := fixture(t)
	if err := os.WriteFile(p.Audit, []byte("article_id,audit_reason\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := datasetreview.Open(p, nil); !errors.Is(err, datasetreview.ErrEmptyAudit) {
		t.Errorf("empty audit error = %v", err)
	}
	if err := os.WriteFile(p.Audit, []byte("article_id,audit_reason\nzz,x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := datasetreview.Open(p, nil)
	if !errors.Is(err, datasetreview.ErrAbsentFromDataset) || err.Error() != "audit article zz is absent from the dataset" {
		t.Errorf("absent row error = %v", err)
	}
}

func TestPendingHidesLogged(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	if len(s.Pending()) != 2 {
		t.Fatalf("pending = %d, want 2", len(s.Pending()))
	}
	entry, _ := s.Apply("a1", datasetreview.Confirm, "rev", testNow)
	if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	pending := s.Pending()
	if len(pending) != 1 || pending[0].ArticleID != "a2" {
		t.Fatalf("pending = %+v, want only a2", pending)
	}
	if reopened := open(t, p).Pending(); len(reopened) != 1 || reopened[0].ArticleID != "a2" {
		t.Fatalf("reopened pending = %+v, want only a2", reopened)
	}
}

func TestApplyFieldRules(t *testing.T) {
	cases := []struct {
		d     datasetreview.Decision
		id    string
		label string
	}{
		{datasetreview.Publish, "a1", "publish"},
		{datasetreview.Reject, "a2", "reject"},
		{datasetreview.Confirm, "a2", "publish"},
	}
	for _, tc := range cases {
		s := open(t, fixture(t))
		before := map[string]editorialtrain.TrainingExample{}
		for _, ex := range s.Examples {
			before[ex.ArticleID] = ex
		}
		entry, err := s.Apply(tc.id, tc.d, "rev", testNow)
		if err != nil {
			t.Fatal(err)
		}
		var got editorialtrain.TrainingExample
		for _, ex := range s.Examples {
			if ex.ArticleID == tc.id {
				got = ex
			}
		}
		prev := before[tc.id]
		if got.Label != tc.label || got.LabelSource != "interactive_review" || got.Reason != "interactive_dataset_review" || !got.LabeledAt.Equal(testNow) || got.Revision != prev.Revision+1 {
			t.Errorf("%s: example = %+v", tc.d, got)
		}
		want := datasetreview.LogEntry{ArticleID: tc.id, Previous: prev.Label, Label: tc.label, Audit: map[string]string{"a1": "r1", "a2": "r2"}[tc.id], Reviewer: "rev", Reviewed: testNow}
		if entry != want {
			t.Errorf("%s: entry = %+v, want %+v", tc.d, entry, want)
		}
	}
	s := open(t, fixture(t))
	if _, err := s.Apply("nope", datasetreview.Publish, "rev", testNow); !errors.Is(err, datasetreview.ErrNotInAudit) {
		t.Errorf("unknown id error = %v", err)
	}
}

func TestSaveWritesCopyManifestAndLog(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	entry, _ := s.Apply("a1", datasetreview.Publish, "rev", testNow)
	if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Out + ".manifest.json"); err != nil {
		t.Errorf("manifest: %v", err)
	}
	entries, err := datasetreview.ReadLog(p.Log())
	if err != nil || len(entries) != 1 || entries[0] != entry {
		t.Fatalf("ReadLog() = %+v, %v; want [%+v]", entries, err, entry)
	}
	copyDS, err := editorialtrain.LoadDataset(p.Out)
	if err != nil || copyDS.Examples[0].Label != "publish" {
		t.Fatalf("copy = %+v, %v", copyDS.Examples, err)
	}
	src, _ := editorialtrain.LoadDataset(p.Dataset)
	if src.Examples[0].Label != "reject" {
		t.Error("source dataset must stay unchanged")
	}
}

func TestTwoSavesKeepBoth(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	for _, step := range []struct {
		id string
		d  datasetreview.Decision
	}{{"a1", datasetreview.Publish}, {"a2", datasetreview.Reject}} {
		entry, err := s.Apply(step.id, step.d, "rev", testNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := editorialtrain.LoadDataset(p.Out)
	if got.Examples[0].Label != "publish" || got.Examples[1].Label != "reject" {
		t.Errorf("copy labels = %s, %s; want publish, reject", got.Examples[0].Label, got.Examples[1].Label)
	}
	entries, err := datasetreview.ReadLog(p.Log())
	if err != nil || len(entries) != 2 {
		t.Fatalf("log = %+v, %v; want 2 entries", entries, err)
	}
}

func TestReadLogMissingFileIsEmpty(t *testing.T) {
	entries, err := datasetreview.ReadLog(filepath.Join(t.TempDir(), "none.csv"))
	if err != nil || len(entries) != 0 {
		t.Errorf("ReadLog() = %v, %v", entries, err)
	}
}

func TestReadAuditCandidatesErrors(t *testing.T) {
	if _, err := datasetreview.ReadAuditCandidates(strings.NewReader("a,b\n1,2\n")); err == nil || !strings.Contains(err.Error(), "article_id and audit_reason") {
		t.Errorf("missing columns error = %v", err)
	}
	if _, err := datasetreview.ReadAuditCandidates(strings.NewReader("article_id,audit_reason\n ,x\n")); err == nil || !strings.Contains(err.Error(), "empty article_id") {
		t.Errorf("empty id error = %v", err)
	}
}

func TestApplyRejectsUnknownDecision(t *testing.T) {
	s := open(t, fixture(t))
	if _, err := s.Apply("a1", datasetreview.Decision("bogus"), "rev", testNow); !errors.Is(err, datasetreview.ErrInvalidDecision) {
		t.Fatalf("err = %v, want ErrInvalidDecision", err)
	}
	if s.Examples[0].Revision != 1 || s.Examples[0].Label != "reject" {
		t.Fatalf("example mutated: %+v", s.Examples[0])
	}
}

func TestApplyPendingRefusesLogged(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	entry, err := s.ApplyPending("a1", datasetreview.Publish, "rev", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPending("a1", datasetreview.Reject, "rev", testNow); !errors.Is(err, datasetreview.ErrAlreadyReviewed) {
		t.Fatalf("err = %v, want ErrAlreadyReviewed", err)
	}
	if _, err := open(t, p).ApplyPending("a1", datasetreview.Reject, "rev", testNow); !errors.Is(err, datasetreview.ErrAlreadyReviewed) {
		t.Fatalf("reopened err = %v, want ErrAlreadyReviewed", err)
	}
	// Apply stays unguarded: the CLI re-shows logged rows across sessions.
	if _, err := s.Apply("a1", datasetreview.Reject, "rev", testNow); err != nil {
		t.Fatalf("Apply on logged row: %v", err)
	}
}

func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	p := fixture(t)
	s := open(t, p)
	entry, _ := s.Apply("a1", datasetreview.Publish, "rev", testNow)
	if err := s.Save(testNow, []datasetreview.LogEntry{entry}); err != nil {
		t.Fatal(err)
	}
	copyInfo, _ := os.Stat(p.Out)
	manifestInfo, _ := os.Stat(p.Out + ".manifest.json")
	entry2, _ := s.Apply("a2", datasetreview.Reject, "rev", testNow)
	if err := s.Save(testNow, []datasetreview.LogEntry{entry2}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(p.Out); os.SameFile(copyInfo, after) {
		t.Error("copy was rewritten in place; want a temp file renamed over it")
	}
	if after, _ := os.Stat(p.Out + ".manifest.json"); os.SameFile(manifestInfo, after) {
		t.Error("manifest was rewritten in place; want a temp file renamed over it")
	}
	if after, _ := os.Stat(p.Out); after.Mode().Perm() != 0o600 {
		t.Errorf("copy mode = %v, want 0600", after.Mode().Perm())
	}
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(p.Out), "*"))
	if len(names) != 5 { // dataset, audit, copy, manifest, log
		t.Errorf("dir files = %v, want exactly the five session files", names)
	}
}
