package entitycycle_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Discover runs discovery and prints its counters, but never offers the
// proposals: they stay pending for another reviewer (the admin panel).
func TestDiscoverOnlyDoesNotReview(t *testing.T) {
	e := newEnv(t)
	e.llm.entities = []appout.EntityDiscoveryEntity{newcomer("Random Newcomer", e.ids[0])}
	var out bytes.Buffer
	res, err := entitycycle.Discover(context.Background(), e.deps, entitycycle.Input{
		SessionArticleIDs: e.ids, ExtractorVersion: "v-test", BatchSize: 5, Limit: 100, VerifyMinConfidence: 0.85, Out: &out,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if res.Proposed != 1 || res.Approved != 0 || res.Paused || res.Regenerated {
		t.Fatalf("result = %+v", res)
	}
	want := "entities: 2 articles, 1 batches (0 failed), 1 proposals created or updated\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	pending, err := e.repo.List(context.Background(), "", string(entityproposal.StatusPending))
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d (err %v), want the proposal left pending", len(pending), err)
	}
	if len(e.applied) != 0 || e.regen != 0 {
		t.Fatalf("applied=%v regen=%d, want nothing applied or regenerated", e.applied, e.regen)
	}
}

func TestDiscoverWarnsWhenEveryBatchFailed(t *testing.T) {
	e := newEnv(t)
	e.llm.err = context.DeadlineExceeded
	var out bytes.Buffer
	res, err := entitycycle.Discover(context.Background(), e.deps, entitycycle.Input{
		SessionArticleIDs: e.ids, ExtractorVersion: "v-test", BatchSize: 5, Limit: 100, Out: &out,
	})
	if err != nil || !res.DiscoveryFailed {
		t.Fatalf("res=%+v err=%v, want a failed discovery reported as a warning", res, err)
	}
}

// FinishReview regenerates the catalog only when an approval edited it.
func TestFinishReviewRegeneratesWhenDirty(t *testing.T) {
	e := newEnv(t)
	regenerated, err := entitycycle.FinishReview(context.Background(), e.deps, false)
	if err != nil || regenerated || e.regen != 0 {
		t.Fatalf("FinishReview(false) = %v, %v (regen %d), want no regeneration", regenerated, err, e.regen)
	}
	regenerated, err = entitycycle.FinishReview(context.Background(), e.deps, true)
	if err != nil || !regenerated || e.regen != 1 {
		t.Fatalf("FinishReview(true) = %v, %v (regen %d), want one regeneration", regenerated, err, e.regen)
	}
	e.deps.Regenerate = func() error { return context.Canceled }
	if _, err := entitycycle.FinishReview(context.Background(), e.deps, true); err == nil {
		t.Fatal("FinishReview must return the regeneration error")
	}
}
