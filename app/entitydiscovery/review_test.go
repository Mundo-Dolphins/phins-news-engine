package entitydiscovery_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
)

// reviewRepo is an in-memory EntityProposalRepository that only
// implements what Approve and Reject use.
type reviewRepo struct {
	entityproposalport.EntityProposalRepository
	rows map[string]entityproposal.Proposal
}

func newReviewRepo(ps ...entityproposal.Proposal) *reviewRepo {
	r := &reviewRepo{rows: map[string]entityproposal.Proposal{}}
	for _, p := range ps {
		r.rows[p.ID] = p
	}
	return r
}

func (r *reviewRepo) Get(_ context.Context, id string) (entityproposal.Proposal, error) {
	p, ok := r.rows[id]
	if !ok {
		return entityproposal.Proposal{}, entityproposal.ErrNotFound
	}
	return p, nil
}

func (r *reviewRepo) UpdateStatus(_ context.Context, id string, status entityproposal.Status, at time.Time) error {
	p, ok := r.rows[id]
	if !ok {
		return entityproposal.ErrNotFound
	}
	p.Status, p.DecidedAt = status, at
	r.rows[id] = p
	return nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestApproveNotPending(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	repo := newReviewRepo(entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", Kind: entityproposal.KindNewEntity,
		EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusApproved,
		Metadata: map[string]string{"team_id": "springfield-isotopes"},
	})
	dictBefore, manualBefore := readFile(t, paths.DictionaryPath), readFile(t, paths.ManualCatalogPath)

	_, err := entitydiscovery.Approve(context.Background(), repo, "p1", entitydiscovery.Resolution{}, paths, time.Now().UTC())
	if !errors.Is(err, entitydiscovery.ErrNotPending) {
		t.Fatalf("Approve() error = %v, want ErrNotPending", err)
	}
	if want := "proposal p1 is already approved"; err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if readFile(t, paths.DictionaryPath) != dictBefore || readFile(t, paths.ManualCatalogPath) != manualBefore {
		t.Fatal("catalog files changed")
	}
	if repo.rows["p1"].Status != entityproposal.StatusApproved || !repo.rows["p1"].DecidedAt.IsZero() {
		t.Fatalf("row changed: %+v", repo.rows["p1"])
	}
}

func TestApprovePossibleDuplicateNeedsResolution(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	repo := newReviewRepo(entityproposal.Proposal{
		ID: "p1", CanonicalName: "John Smith", Kind: entityproposal.KindPossibleDuplicate,
		EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending,
	})
	dictBefore := readFile(t, paths.DictionaryPath)

	_, err := entitydiscovery.Approve(context.Background(), repo, "p1", entitydiscovery.Resolution{}, paths, time.Now().UTC())
	if !errors.Is(err, entitydiscovery.ErrNeedsResolution) {
		t.Fatalf("Approve() error = %v, want ErrNeedsResolution", err)
	}
	if want := "proposal p1 is a possible duplicate — pass --merge-into <entity-id> or --as-new"; err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if readFile(t, paths.DictionaryPath) != dictBefore || repo.rows["p1"].Status != entityproposal.StatusPending {
		t.Fatal("state changed")
	}
}

func TestApproveBothResolutions(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	repo := newReviewRepo(entityproposal.Proposal{
		ID: "p1", CanonicalName: "John Smith", Kind: entityproposal.KindPossibleDuplicate,
		EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending,
	})
	_, err := entitydiscovery.Approve(context.Background(), repo, "p1",
		entitydiscovery.Resolution{MergeInto: "springfield-isotopes", AsNew: true}, paths, time.Now().UTC())
	if !errors.Is(err, entitydiscovery.ErrConflictingResolution) {
		t.Fatalf("Approve() error = %v, want ErrConflictingResolution", err)
	}
	if want := "pass only one of --merge-into or --as-new"; err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if repo.rows["p1"].Status != entityproposal.StatusPending {
		t.Fatal("status changed")
	}
}

func TestApproveMergeIntoBecomesAlias(t *testing.T) {
	paths := writeTestCatalogFiles(t)
	repo := newReviewRepo(entityproposal.Proposal{
		ID: "p1", CanonicalName: "Isotopes FC", Kind: entityproposal.KindPossibleDuplicate,
		EntityType: entityproposal.EntityTypeTeam, Status: entityproposal.StatusPending,
	})
	got, err := entitydiscovery.Approve(context.Background(), repo, "p1",
		entitydiscovery.Resolution{MergeInto: "springfield-isotopes"}, paths, time.Now().UTC())
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Kind != entityproposal.KindNewAlias || got.TargetEntityID != "springfield-isotopes" {
		t.Fatalf("resolved = kind %q target %q", got.Kind, got.TargetEntityID)
	}
	if repo.rows["p1"].Status != entityproposal.StatusApproved {
		t.Fatalf("status = %q, want approved", repo.rows["p1"].Status)
	}
}

func TestRejectUnknownID(t *testing.T) {
	_, err := entitydiscovery.Reject(context.Background(), newReviewRepo(), "nope", time.Now().UTC())
	if !errors.Is(err, entityproposal.ErrNotFound) {
		t.Fatalf("Reject() error = %v, want ErrNotFound", err)
	}
}

func TestRejectMarksRejectedAndRefusesDecided(t *testing.T) {
	repo := newReviewRepo(entityproposal.Proposal{ID: "p1", Status: entityproposal.StatusPending})
	if _, err := entitydiscovery.Reject(context.Background(), repo, "p1", time.Now().UTC()); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if repo.rows["p1"].Status != entityproposal.StatusRejected {
		t.Fatalf("status = %q", repo.rows["p1"].Status)
	}
	_, err := entitydiscovery.Reject(context.Background(), repo, "p1", time.Now().UTC())
	if !errors.Is(err, entitydiscovery.ErrNotPending) || err.Error() != "proposal p1 is already rejected" {
		t.Fatalf("second Reject() error = %v", err)
	}
}
