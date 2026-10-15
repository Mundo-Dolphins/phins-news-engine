package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

type fakeCheckpointer struct {
	called bool
	err    error
}

func (c *fakeCheckpointer) Checkpoint(context.Context) error {
	c.called = true
	return c.err
}

func TestRunRetention_DeletesEachCategoryWithItsOwnWindow(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	type call struct {
		statuses []article.Status
		cutoff   time.Time
	}
	var calls []call
	repo := &fakeArticleRepo{
		deleteByStatus: func(statuses []article.Status, cutoff time.Time) (int, error) {
			calls = append(calls, call{statuses, cutoff})
			if len(statuses) == 1 && statuses[0] == article.StatusRejectedDuplicate {
				return 3, nil
			}
			return 5, nil
		},
	}
	checkpointer := &fakeCheckpointer{}
	windows := in.RetentionWindows{
		RejectedArticles:    14 * 24 * time.Hour,
		Duplicates:          7 * 24 * time.Hour,
		AuditEvents:         30 * 24 * time.Hour,
		PublicationAttempts: 30 * 24 * time.Hour,
		Clusters:            14 * 24 * time.Hour,
	}
	labels := &fakeLabelRepo{editorialCount: 7}
	uc := usecase.NewRunRetention(repo, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, labels, checkpointer, windows, fakeClock{now: now}, fakeLogger{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.DuplicatesDeleted != 3 || result.RejectedArticlesDeleted != 5 {
		t.Errorf("result = %+v, want duplicates=3 rejected=5", result)
	}
	if result.EditorialExamplesRetained != 7 {
		t.Errorf("result.EditorialExamplesRetained = %d, want 7", result.EditorialExamplesRetained)
	}
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	if wantDup := now.Add(-windows.Duplicates); !calls[0].cutoff.Equal(wantDup) {
		t.Errorf("duplicates cutoff = %v, want %v", calls[0].cutoff, wantDup)
	}
	if wantRej := now.Add(-windows.RejectedArticles); !calls[1].cutoff.Equal(wantRej) {
		t.Errorf("rejected cutoff = %v, want %v", calls[1].cutoff, wantRej)
	}
	if !checkpointer.called {
		t.Error("expected Checkpoint to be called")
	}
}

func TestRunRetention_NeverTargetsPublishedArticles(t *testing.T) {
	var allStatuses []article.Status
	repo := &fakeArticleRepo{
		deleteByStatus: func(statuses []article.Status, _ time.Time) (int, error) {
			allStatuses = append(allStatuses, statuses...)
			return 0, nil
		},
	}
	uc := usecase.NewRunRetention(repo, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, &fakeLabelRepo{}, &fakeCheckpointer{}, in.RetentionWindows{}, fakeClock{now: time.Now()}, fakeLogger{})

	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, s := range allStatuses {
		if s == article.StatusPublished {
			t.Fatalf("StatusPublished appeared in a deletable status list: %v", allStatuses)
		}
	}
}

func TestRunRetention_CheckpointFailureDoesNotFailRun(t *testing.T) {
	now := time.Now()
	repo := &fakeArticleRepo{
		deleteByStatus: func(statuses []article.Status, _ time.Time) (int, error) {
			if len(statuses) == 1 && statuses[0] == article.StatusRejectedDuplicate {
				return 2, nil
			}
			return 4, nil
		},
	}
	checkpointer := &fakeCheckpointer{err: errors.New("wal checkpoint busy")}
	uc := usecase.NewRunRetention(repo, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, &fakeLabelRepo{}, checkpointer, in.RetentionWindows{}, fakeClock{now: now}, fakeLogger{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v, want nil even when checkpoint fails", err)
	}
	if result.DuplicatesDeleted != 2 || result.RejectedArticlesDeleted != 4 {
		t.Errorf("result = %+v, want deletions to still be reported despite checkpoint failure", result)
	}
	if !checkpointer.called {
		t.Error("expected Checkpoint to have been attempted")
	}
}

func TestRunRetention_WrapsAttemptsError(t *testing.T) {
	repoErr := errors.New("disk full")
	outbox := &fakeOutboxRepo{deleteOldAttemptsErr: repoErr}
	uc := usecase.NewRunRetention(&fakeArticleRepo{}, &fakeDecisionRepo{}, outbox, &fakeClusterRepo{}, &fakeLabelRepo{}, &fakeCheckpointer{}, in.RetentionWindows{}, fakeClock{now: time.Now()}, fakeLogger{})

	_, err := uc.Run(context.Background())
	if err == nil || !errors.Is(err, repoErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, repoErr)
	}
}

func TestRunRetention_EditorialExamplesCountFailureDoesNotFailRun(t *testing.T) {
	countErr := errors.New("db locked")
	labels := &fakeLabelRepo{countErr: countErr}
	uc := usecase.NewRunRetention(&fakeArticleRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, labels, &fakeCheckpointer{}, in.RetentionWindows{}, fakeClock{now: time.Now()}, fakeLogger{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v, want nil even when the editorial examples count fails", err)
	}
	if result.EditorialExamplesRetained != 0 {
		t.Errorf("result.EditorialExamplesRetained = %d, want 0 (count failed, so it stays at its zero value)", result.EditorialExamplesRetained)
	}
}
