package classifierdictionary

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/classifierproposalport"
)

// fakeClassifierRepo is an in-memory repository implementing only what
// Approve and Reject use.
type fakeClassifierRepo struct {
	classifierproposalport.ClassifierProposalRepository
	rows map[string]classifierproposal.Proposal
}

func (r *fakeClassifierRepo) Get(_ context.Context, id string) (classifierproposal.Proposal, error) {
	p, ok := r.rows[id]
	if !ok {
		return classifierproposal.Proposal{}, classifierproposal.ErrNotFound
	}
	return p, nil
}

func (r *fakeClassifierRepo) UpdateStatus(_ context.Context, id string, status classifierproposal.Status, at time.Time) error {
	p := r.rows[id]
	p.Status, p.DecidedAt = status, at
	r.rows[id] = p
	return nil
}

func TestApproveWritesDictionaryAndStatus(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "dictionary.yml")
	seed := dictionary.Dictionary{
		Version: "1.0.0", Name: "test", Language: "en", SchemaVersion: 2,
		Categories: []dictionary.Category{{Name: "nfl_people_context", Weight: 2}},
		Decision:   dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 7, MaxCountPerTerm: 3, TitleWeightMultiplier: 1},
	}
	seed.Checksum = dictionary.ContentHash(seed)
	if err := dictionaryfile.Save(path, seed); err != nil {
		t.Fatalf("Save(seed) error = %v", err)
	}
	repo := &fakeClassifierRepo{rows: map[string]classifierproposal.Proposal{"p1": {
		ID: "p1", Kind: classifierproposal.KindTerm, Category: "nfl_people_context",
		Value: "Jai Rene Mossbank", NormalizedValue: "jai rene mossbank", Operation: "add",
		Confidence: 0.8, Occurrences: 3, Status: classifierproposal.StatusPending,
	}}}

	got, err := Approve(context.Background(), repo, "p1", path, now)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.ID != "p1" || got.Value != "Jai Rene Mossbank" {
		t.Fatalf("returned proposal = %+v", got)
	}
	if repo.rows["p1"].Status != classifierproposal.StatusApproved {
		t.Fatalf("status = %q, want approved", repo.rows["p1"].Status)
	}
	d, err := dictionaryfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	found := false
	for _, c := range d.Categories {
		for _, e := range c.Entries {
			found = found || e.CanonicalName == "Jai Rene Mossbank"
		}
	}
	if !found {
		t.Fatal("value not written to the dictionary")
	}
}

func TestRejectNotPending(t *testing.T) {
	repo := &fakeClassifierRepo{rows: map[string]classifierproposal.Proposal{"p1": {ID: "p1", Status: classifierproposal.StatusRejected}}}
	_, err := Reject(context.Background(), repo, "p1", time.Now().UTC())
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("Reject() error = %v, want ErrNotPending", err)
	}
	if want := "proposal p1 is already rejected"; err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if _, err := Reject(context.Background(), repo, "missing", time.Now().UTC()); !errors.Is(err, classifierproposal.ErrNotFound) {
		t.Fatalf("Reject(missing) error = %v, want ErrNotFound", err)
	}
}
