package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// repairArticles builds a fakeArticleRepo holding one article "a1" with
// clusterID (pass "" for an unclustered article) — every RepairOutbox.Run
// call now fetches the article before it writes anything.
func repairArticles(clusterID string) *fakeArticleRepo {
	return &fakeArticleRepo{byID: map[string]article.Article{
		"a1": {ID: "a1", ClusterID: clusterID, Status: article.StatusPublishing},
	}}
}

// repairGate builds a fakeClusterPublicationRepository with clusterID
// already 'reserved' by article "a1" — the state a quarantined item leaves
// behind, and the one a manual repair must resolve.
func repairGate(clusterID string) *fakeClusterPublicationRepository {
	gate := &fakeClusterPublicationRepository{}
	if clusterID != "" {
		if _, err := gate.Reserve(context.Background(), out.ClusterPublicationReservation{ClusterID: clusterID, ArticleID: "a1"}, time.Time{}); err != nil {
			panic(err)
		}
	}
	return gate
}

func TestRepairOutbox_ValidActionAppliesAndAudits(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	r := usecase.NewRepairOutbox(outbox, repairArticles(""), repairGate(""), decisions, &fakeUnitOfWork{}, fakeClock{now: now}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "confirmed via channel"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(outbox.repaired) != 1 || outbox.repaired[0] != 7 {
		t.Errorf("outbox.repaired = %v, want [7]", outbox.repaired)
	}
	if len(decisions.events) != 1 || decisions.events[0].EventType != "outbox_manual_repair" {
		t.Errorf("decisions.events = %+v, want 1 outbox_manual_repair event", decisions.events)
	}
}

// TestRepairOutbox_DeliverConfirmsClusterGate is the deliver half of the
// fix for the wedge the whole-branch review found: a terminal repair that
// never touches event_cluster_publications leaves the cluster 'reserved'
// forever, and Reserve only ever succeeds on a 'released' row.
func TestRepairOutbox_DeliverConfirmsClusterGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	articles := repairArticles("c1")
	gate := repairGate("c1")
	decisions := &fakeDecisionRepo{}
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, articles, gate, decisions, &fakeUnitOfWork{}, fakeClock{now: now}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "seen in the channel"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := articles.lastSaved().Status; got != article.StatusPublished {
		t.Errorf("article status = %q, want %q", got, article.StatusPublished)
	}
	if got := articles.lastSaved().StatusReason; got != "" {
		t.Errorf("article status reason = %q, want empty", got)
	}
	if len(gate.confirmed) != 1 || gate.confirmed[0] != "c1" {
		t.Errorf("gate.confirmed = %v, want [c1]", gate.confirmed)
	}
	if len(gate.released) != 0 {
		t.Errorf("gate.released = %v, want none", gate.released)
	}
	if gate.state["c1"] != "confirmed" {
		t.Errorf("gate state = %q, want confirmed", gate.state["c1"])
	}
	if len(decisions.events) != 1 ||
		decisions.events[0].FromStatus != article.StatusPublishing ||
		decisions.events[0].ToStatus != article.StatusPublished {
		t.Errorf("decisions.events = %+v, want one publishing->published event", decisions.events)
	}
}

// TestRepairOutbox_DiscardReleasesClusterGate is the discard half: the
// article never made it out, so the cluster must be freed for a future
// article to claim the initial-publication slot.
func TestRepairOutbox_DiscardReleasesClusterGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	articles := repairArticles("c1")
	gate := repairGate("c1")
	decisions := &fakeDecisionRepo{}
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, articles, gate, decisions, &fakeUnitOfWork{}, fakeClock{now: now}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDiscard, "a1", "never delivered, giving up"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := articles.lastSaved().Status; got != article.StatusDiscarded {
		t.Errorf("article status = %q, want %q", got, article.StatusDiscarded)
	}
	if len(gate.released) != 1 || gate.released[0] != "c1" {
		t.Errorf("gate.released = %v, want [c1]", gate.released)
	}
	if len(gate.confirmed) != 0 {
		t.Errorf("gate.confirmed = %v, want none", gate.confirmed)
	}
	if gate.state["c1"] != "released" {
		t.Errorf("gate state = %q, want released", gate.state["c1"])
	}
	// The freed cluster must now be re-reservable by another article —
	// the whole point of releasing it.
	acquired, err := gate.Reserve(context.Background(), out.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2"}, now)
	if err != nil || !acquired {
		t.Errorf("Reserve() = %v, %v, want true, nil after a discard repair released the gate", acquired, err)
	}
	if len(decisions.events) != 1 ||
		decisions.events[0].FromStatus != article.StatusPublishing ||
		decisions.events[0].ToStatus != article.StatusDiscarded {
		t.Errorf("decisions.events = %+v, want one publishing->discarded event", decisions.events)
	}
}

// TestRepairOutbox_RetryLeavesClusterGateReserved: retry returns the item
// to the normal publish flow, which will Confirm or Release the gate
// through the existing paths — repairing it here would let a second
// article publish for the same event while this one is still live.
func TestRepairOutbox_RetryLeavesClusterGateReserved(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	articles := repairArticles("c1")
	gate := repairGate("c1")
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, articles, gate, &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{now: now}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairRetry, "a1", "operator says try again"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(gate.confirmed) != 0 || len(gate.released) != 0 {
		t.Errorf("gate.confirmed = %v, gate.released = %v, want neither touched", gate.confirmed, gate.released)
	}
	if gate.state["c1"] != "reserved" {
		t.Errorf("gate state = %q, want reserved", gate.state["c1"])
	}
	if len(articles.saved) != 0 {
		t.Errorf("articles.saved = %+v, want none (retry leaves the article's status to the next publish attempt)", articles.saved)
	}
}

// TestRepairOutbox_UnclusteredArticleSkipsGate: an article with no
// ClusterID has no gate row to resolve, exactly like finalizeDelivered and
// recordFailure guard on a.ClusterID != "".
func TestRepairOutbox_UnclusteredArticleSkipsGate(t *testing.T) {
	for _, action := range []publishing.RepairAction{publishing.RepairDeliver, publishing.RepairDiscard} {
		t.Run(string(action), func(t *testing.T) {
			gate := repairGate("")
			r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, repairArticles(""), gate, &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})
			if err := r.Run(context.Background(), 7, action, "a1", "reason"); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(gate.confirmed) != 0 || len(gate.released) != 0 {
				t.Errorf("gate.confirmed = %v, gate.released = %v, want neither for an unclustered article", gate.confirmed, gate.released)
			}
		})
	}
}

func TestRepairOutbox_PropagatesMissingArticle(t *testing.T) {
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	articles := &fakeArticleRepo{findErr: errors.New("db gone")}
	r := usecase.NewRepairOutbox(outbox, articles, repairGate(""), decisions, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "reason"); err == nil {
		t.Fatal("Run() error = nil, want an error when the article can't be loaded")
	}
	if len(outbox.repaired) != 0 || len(decisions.events) != 0 {
		t.Errorf("repaired = %v, events = %v, want none (a failed article lookup must have no side effects)", outbox.repaired, decisions.events)
	}
}

func TestRepairOutbox_RejectsInvalidAction(t *testing.T) {
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, repairArticles(""), repairGate(""), &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})
	err := r.Run(context.Background(), 7, publishing.RepairAction("bogus"), "a1", "reason")
	if err == nil {
		t.Fatal("Run() error = nil, want a validation error for an unrecognized action")
	}
}

func TestRepairOutbox_RequiresReason(t *testing.T) {
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, repairArticles(""), repairGate(""), &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})
	err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "")
	if err == nil {
		t.Fatal("Run() error = nil, want an error: a manual override with no operator reason must be rejected")
	}
}

func TestRepairOutbox_PropagatesNotRepairable(t *testing.T) {
	outbox := &fakeOutboxRepo{repairErr: out.ErrNotRepairable}
	r := usecase.NewRepairOutbox(outbox, repairArticles(""), repairGate(""), &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})
	err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "reason")
	if !errors.Is(err, out.ErrNotRepairable) {
		t.Fatalf("Run() error = %v, want ErrNotRepairable", err)
	}
}

func TestRepairOutbox_RejectsInvalidActionWithoutSideEffects(t *testing.T) {
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	gate := repairGate("c1")
	articles := repairArticles("c1")
	r := usecase.NewRepairOutbox(outbox, articles, gate, decisions, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairAction("bogus"), "a1", "reason"); err == nil {
		t.Fatal("Run() error = nil, want a validation error")
	}
	if len(outbox.repaired) != 0 {
		t.Errorf("outbox.repaired = %v, want none (invalid action must not touch the repository)", outbox.repaired)
	}
	if len(decisions.events) != 0 {
		t.Errorf("decisions.events = %v, want none (invalid action must not touch the repository)", decisions.events)
	}
	if len(articles.saved) != 0 || len(gate.confirmed) != 0 || len(gate.released) != 0 {
		t.Errorf("articles.saved = %v, gate = %v/%v, want none", articles.saved, gate.confirmed, gate.released)
	}
}

func TestRepairOutbox_RequiresReasonWithoutSideEffects(t *testing.T) {
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{}
	gate := repairGate("c1")
	articles := repairArticles("c1")
	r := usecase.NewRepairOutbox(outbox, articles, gate, decisions, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", ""); err == nil {
		t.Fatal("Run() error = nil, want an error")
	}
	if len(outbox.repaired) != 0 {
		t.Errorf("outbox.repaired = %v, want none (empty reason must not touch the repository)", outbox.repaired)
	}
	if len(decisions.events) != 0 {
		t.Errorf("decisions.events = %v, want none (empty reason must not touch the repository)", decisions.events)
	}
	if len(articles.saved) != 0 || len(gate.confirmed) != 0 || len(gate.released) != 0 {
		t.Errorf("articles.saved = %v, gate = %v/%v, want none", articles.saved, gate.confirmed, gate.released)
	}
}

func TestRepairOutbox_RepairAndAppendAreAtomic(t *testing.T) {
	// decisions.Append fails after outbox.Repair has already succeeded: the
	// whole Run must fail and, since both live inside the same uow.Do
	// transaction, a real (non-fake) UnitOfWork would roll the Repair back
	// too. Here we assert Run reports the failure — the fake UnitOfWork
	// itself doesn't simulate rollback, but the shared uow.Do call is what
	// gives a real implementation that guarantee.
	outbox := &fakeOutboxRepo{}
	decisions := &fakeDecisionRepo{appendErr: errors.New("db gone")}
	r := usecase.NewRepairOutbox(outbox, repairArticles(""), repairGate(""), decisions, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})

	err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "reason")
	if err == nil {
		t.Fatal("Run() error = nil, want an error when the audit append fails")
	}
}

// TestRepairOutbox_PropagatesClusterGateFailure: the gate write lives
// inside the same uow.Do transaction as the repair itself, so a gate
// failure must fail the whole repair rather than leaving the outbox
// repaired and the cluster wedged.
func TestRepairOutbox_PropagatesClusterGateFailure(t *testing.T) {
	gate := repairGate("c1")
	gate.confirmErr = errors.New("gate write failed")
	r := usecase.NewRepairOutbox(&fakeOutboxRepo{}, repairArticles("c1"), gate, &fakeDecisionRepo{}, &fakeUnitOfWork{}, fakeClock{}, fakeLogger{})

	if err := r.Run(context.Background(), 7, publishing.RepairDeliver, "a1", "reason"); err == nil {
		t.Fatal("Run() error = nil, want the cluster-gate failure propagated")
	}
}
