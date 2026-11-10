// Package datasetreview holds the file logic of the human review of a
// dataset audit CSV, shared by the `train review-dataset` CLI and the admin
// panel: reading the audit, resuming a reviewed copy, applying one decision
// and saving the copy, its manifest and the decision log.
package datasetreview

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

const (
	// DefaultDataset is the default source dataset path.
	DefaultDataset = "data/editorial_dataset.yml"
	// DefaultAudit is the default prioritized audit CSV path.
	DefaultAudit = "data/editorial_dataset-review.csv"
	// DefaultOut is the default path of the reviewed dataset copy.
	DefaultOut = "data/editorial_dataset-reviewed.yml"
)

// Decision is a reviewer's verdict on a dataset row.
type Decision string

const (
	// Publish relabels the row "publish".
	Publish Decision = "publish"
	// Reject relabels the row "reject".
	Reject Decision = "reject"
	// Confirm keeps the row's current label.
	Confirm Decision = "confirm"
)

var (
	// ErrSamePath means the reviewed copy would overwrite the source dataset.
	ErrSamePath = errors.New("--out must differ from --dataset; original dataset is preserved")
	// ErrEmptyAudit means the audit CSV has no rows.
	ErrEmptyAudit = errors.New("audit CSV contains no rows")
	// ErrResumeMismatch means an existing reviewed copy does not match the
	// source dataset snapshot.
	ErrResumeMismatch = errors.New("review output does not match source snapshot")
	// ErrAbsentFromDataset means an audit row is not in the dataset.
	ErrAbsentFromDataset = errors.New("audit article is absent from the dataset")
	// ErrNotInAudit means a decision targets an article that is not an audit row.
	ErrNotInAudit = errors.New("article is not in the audit")
	// ErrAlreadyReviewed means the article already has a logged decision.
	ErrAlreadyReviewed = errors.New("already reviewed")
	// ErrInvalidDecision means a decision is not publish, reject or confirm.
	ErrInvalidDecision = errors.New("decision must be publish, reject or confirm")
)

// kindError carries a full message and the sentinel it matches via errors.Is.
type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

// Paths are the three files of a review session.
type Paths struct {
	// Dataset is the source dataset (never modified).
	Dataset string
	// Audit is the prioritized audit CSV.
	Audit string
	// Out is the reviewed dataset copy.
	Out string
}

// Log is the decision log path next to the reviewed copy.
func (p Paths) Log() string { return p.Out + ".review.csv" }

// LogEntry is one line of the decision log.
type LogEntry struct {
	ArticleID string    `json:"article_id"`
	Previous  string    `json:"previous_label"`
	Label     string    `json:"reviewed_label"`
	Audit     string    `json:"audit_reason"`
	Reviewer  string    `json:"reviewer"`
	Reviewed  time.Time `json:"reviewed_at"`
}

// ReadAuditCandidates parses an audit CSV into candidates, in file order and
// without repeated article ids.
func ReadAuditCandidates(r io.Reader) ([]datasetaudit.Candidate, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read audit CSV header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")] = i
	}
	idCol, hasID := columns["article_id"]
	reasonCol, hasReason := columns["audit_reason"]
	if !hasID || !hasReason {
		return nil, fmt.Errorf("audit CSV must contain article_id and audit_reason columns")
	}
	var out []datasetaudit.Candidate
	seen := map[string]bool{}
	for line := 2; ; line++ {
		record, readErr := cr.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read audit CSV line %d: %w", line, readErr)
		}
		id := strings.TrimSpace(record[idCol])
		if id == "" {
			return nil, fmt.Errorf("audit CSV line %d has an empty article_id", line)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, datasetaudit.Candidate{ArticleID: id, Reason: record[reasonCol]})
	}
	return out, nil
}

// ValidateResume checks that a reviewed copy still matches the source
// snapshot it was made from.
func ValidateResume(source, reviewed []editorialtrain.TrainingExample) error {
	if len(source) != len(reviewed) {
		return &kindError{fmt.Sprintf("review output has %d rows but source dataset has %d; use a new --out path", len(reviewed), len(source)), ErrResumeMismatch}
	}
	byID := make(map[string]editorialtrain.TrainingExample, len(source))
	for _, ex := range source {
		byID[ex.ArticleID] = ex
	}
	for _, ex := range reviewed {
		prior, ok := byID[ex.ArticleID]
		if !ok || prior.SourceID != ex.SourceID || prior.ContentHash != ex.ContentHash || prior.Title != ex.Title || prior.Body != ex.Body {
			return &kindError{fmt.Sprintf("review output does not match source snapshot at article %s; use a new --out path", ex.ArticleID), ErrResumeMismatch}
		}
	}
	return nil
}

var logHeader = []string{"article_id", "previous_label", "reviewed_label", "audit_reason", "reviewer", "reviewed_at"}

// ReadLog reads the decision log at path; a missing file is an empty log.
func ReadLog(path string) ([]LogEntry, error) {
	f, err := os.Open(path) // #nosec G304 -- path is derived from the operator-provided output path.
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read decision log %s: %w", path, err)
	}
	var entries []LogEntry
	for i, rec := range records {
		if i == 0 {
			continue
		}
		if len(rec) != len(logHeader) {
			return nil, fmt.Errorf("decision log %s line %d has %d fields, want %d", path, i+1, len(rec), len(logHeader))
		}
		at, parseErr := time.Parse(time.RFC3339Nano, rec[5])
		if parseErr != nil {
			return nil, fmt.Errorf("decision log %s line %d: %w", path, i+1, parseErr)
		}
		entries = append(entries, LogEntry{ArticleID: rec[0], Previous: rec[1], Label: rec[2], Audit: rec[3], Reviewer: rec[4], Reviewed: at})
	}
	return entries, nil
}

// AppendLog appends entries to the decision log at path, writing the header
// when the file is new.
func AppendLog(path string, entries []LogEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path is derived from the operator-provided output path.
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		if err := w.Write(logHeader); err != nil {
			return err
		}
	}
	for _, d := range entries {
		if err := w.Write([]string{d.ArticleID, d.Previous, d.Label, d.Audit, d.Reviewer, d.Reviewed.Format(time.RFC3339Nano)}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// Session is an open review: the working copy of the dataset, the audit rows
// and which of them already have a logged decision.
type Session struct {
	// Paths are the session's files.
	Paths Paths
	// Examples is the working copy (the resumed reviewed copy when one exists).
	Examples []editorialtrain.TrainingExample
	// Candidates are the audit rows, in CSV order, without repeats.
	Candidates []datasetaudit.Candidate
	// Logged holds the article ids that already have a decision in the log.
	Logged map[string]bool

	index map[string]int
}

// Open loads a review session. Rows whose source is in excluded are dropped
// from the source and from a resumed copy. An existing Paths.Out must match
// the source snapshot.
func Open(p Paths, excluded map[string]bool) (*Session, error) {
	absIn, err := filepath.Abs(p.Dataset)
	if err != nil {
		return nil, err
	}
	absOut, err := filepath.Abs(p.Out)
	if err != nil {
		return nil, err
	}
	if absIn == absOut {
		return nil, ErrSamePath
	}
	dataset, err := editorialtrain.LoadDataset(p.Dataset)
	if err != nil {
		return nil, err
	}
	dataset = dataset.ExcludeSources(excluded)
	if _, statErr := os.Stat(p.Out); statErr == nil {
		prior, loadErr := editorialtrain.LoadDataset(p.Out)
		if loadErr != nil {
			return nil, fmt.Errorf("load existing review output for resuming: %w", loadErr)
		}
		// Older review outputs may still contain official-source rows
		// that are now excluded from training. Drop those rows before
		// checking the saved review snapshot against the filtered source.
		prior = prior.ExcludeSources(excluded)
		if resumeErr := ValidateResume(dataset.Examples, prior.Examples); resumeErr != nil {
			return nil, resumeErr
		}
		dataset = prior
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	auditFile, err := os.Open(p.Audit) // #nosec G304 -- Audit is an explicit operator-provided input path.
	if err != nil {
		return nil, err
	}
	candidates, readErr := ReadAuditCandidates(auditFile)
	closeErr := auditFile.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(candidates) == 0 {
		return nil, ErrEmptyAudit
	}
	index := make(map[string]int, len(dataset.Examples))
	for i, ex := range dataset.Examples {
		index[ex.ArticleID] = i
	}
	for _, c := range candidates {
		if _, ok := index[c.ArticleID]; !ok {
			return nil, &kindError{fmt.Sprintf("audit article %s is absent from the dataset", c.ArticleID), ErrAbsentFromDataset}
		}
	}
	entries, err := ReadLog(p.Log())
	if err != nil {
		return nil, err
	}
	logged := make(map[string]bool, len(entries))
	for _, e := range entries {
		logged[e.ArticleID] = true
	}
	return &Session{Paths: p, Examples: dataset.Examples, Candidates: candidates, Logged: logged, index: index}, nil
}

// Pending returns the audit rows without a logged decision, in CSV order.
func (s *Session) Pending() []datasetaudit.Candidate {
	var out []datasetaudit.Candidate
	for _, c := range s.Candidates {
		if !s.Logged[c.ArticleID] {
			out = append(out, c)
		}
	}
	return out
}

// ApplyPending is Apply for a row that must not have a logged decision yet,
// so a repeated submit cannot log the same article twice. The CLI re-shows
// logged rows across sessions and uses Apply.
func (s *Session) ApplyPending(articleID string, d Decision, reviewer string, now time.Time) (LogEntry, error) {
	if s.Logged[articleID] {
		return LogEntry{}, fmt.Errorf("%w: %s", ErrAlreadyReviewed, articleID)
	}
	return s.Apply(articleID, d, reviewer, now)
}

// Apply records a decision on an audit row in memory and returns its log
// entry. Nothing is written until Save.
func (s *Session) Apply(articleID string, d Decision, reviewer string, now time.Time) (LogEntry, error) {
	if d != Publish && d != Reject && d != Confirm {
		return LogEntry{}, fmt.Errorf("%w, got %q", ErrInvalidDecision, string(d))
	}
	var candidate *datasetaudit.Candidate
	for i := range s.Candidates {
		if s.Candidates[i].ArticleID == articleID {
			candidate = &s.Candidates[i]
			break
		}
	}
	if candidate == nil {
		return LogEntry{}, fmt.Errorf("%w: %s", ErrNotInAudit, articleID)
	}
	ex := &s.Examples[s.index[articleID]]
	label := ex.Label
	switch d {
	case Publish:
		label = "publish"
	case Reject:
		label = "reject"
	}
	entry := LogEntry{ArticleID: ex.ArticleID, Previous: ex.Label, Label: label, Audit: candidate.Reason, Reviewer: reviewer, Reviewed: now}
	ex.Label = label
	ex.LabelSource = "interactive_review"
	ex.Reason = "interactive_dataset_review"
	ex.LabeledAt = now
	ex.Revision++
	return entry, nil
}

// Save writes the working copy and its manifest, appends entries to the
// decision log and marks them logged.
func (s *Session) Save(now time.Time, entries []LogEntry) error {
	if err := editorialtrain.SaveDataset(s.Paths.Out, s.Examples); err != nil {
		return err
	}
	reloaded, err := editorialtrain.LoadDataset(s.Paths.Out)
	if err != nil {
		return err
	}
	if err := editorialtrain.WriteManifest(s.Paths.Out, s.Examples, reloaded.Hash(), now, 0, 0); err != nil {
		return err
	}
	if err := AppendLog(s.Paths.Log(), entries); err != nil {
		return err
	}
	for _, e := range entries {
		s.Logged[e.ArticleID] = true
	}
	return nil
}
