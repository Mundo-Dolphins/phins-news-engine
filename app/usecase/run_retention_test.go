package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
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

type fakeSweeper struct {
	cutoff time.Time
	keep   map[string]struct{}
	calls  int
	res    out.MediaSweepResult
	err    error
}

func (s *fakeSweeper) DeleteOlderThan(_ context.Context, cutoff time.Time, keep map[string]struct{}) (out.MediaSweepResult, error) {
	s.calls++
	s.cutoff, s.keep = cutoff, keep
	return s.res, s.err
}

type fakeOpenPayloads struct {
	payloads []string
	err      error
}

func (r fakeOpenPayloads) ListOpenPayloads(context.Context) ([]string, error) {
	return r.payloads, r.err
}

func newMediaRetention(sweeper *fakeSweeper, payloads fakeOpenPayloads, retention time.Duration, now time.Time) *usecase.RunRetention {
	uc := usecase.NewRunRetention(&fakeArticleRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, &fakeLabelRepo{}, &fakeCheckpointer{}, in.RetentionWindows{}, fakeClock{now: now}, fakeLogger{})
	uc.SetMediaRetention(sweeper, payloads, retention)
	return uc
}

func TestRunRetention_Media_SweepsOlderThanRetentionAndProtectsOpenRows(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sweeper := &fakeSweeper{res: out.MediaSweepResult{Deleted: 4, BytesDeleted: 400, Protected: 1}}
	payloads := fakeOpenPayloads{payloads: []string{
		`{"text":"a","images":[{"path":"one.jpg","mime":"image/jpeg"},{"path":"two.png","mime":"image/png"}]}`,
		`{}`,
		`{"title":"legacy telegram row","canonical_url":"https://example.test/a"}`,
		`{"text":"b","images":[{"path":"one.jpg","mime":"image/jpeg"}]}`,
	}}
	uc := newMediaRetention(sweeper, payloads, 7*24*time.Hour, now)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-7 * 24 * time.Hour); !sweeper.cutoff.Equal(want) {
		t.Errorf("cutoff = %v, want %v", sweeper.cutoff, want)
	}
	if len(sweeper.keep) != 2 {
		t.Errorf("keep = %v, want one.jpg and two.png", sweeper.keep)
	}
	for _, p := range []string{"one.jpg", "two.png"} {
		if _, ok := sweeper.keep[p]; !ok {
			t.Errorf("keep is missing %s", p)
		}
	}
	if result.MediaDeleted != 4 || result.MediaBytesDeleted != 400 || result.MediaProtected != 1 {
		t.Errorf("result = %+v", result)
	}
}

func TestRunRetention_Media_DoesNotSweepWhenOpenRowsCannotBeListed(t *testing.T) {
	listErr := errors.New("db locked")
	for name, payloads := range map[string]fakeOpenPayloads{
		"list fails":          {err: listErr},
		"payload undecodable": {payloads: []string{"not json"}},
	} {
		t.Run(name, func(t *testing.T) {
			sweeper := &fakeSweeper{}
			uc := newMediaRetention(sweeper, payloads, 7*24*time.Hour, time.Now())
			if _, err := uc.Run(context.Background()); err == nil {
				t.Fatal("Run() error = nil, want the media step to fail")
			}
			if sweeper.calls != 0 {
				t.Fatalf("sweeper called %d times with an unknown reference set", sweeper.calls)
			}
		})
	}
}

func TestRunRetention_Media_SweepErrorIsReported(t *testing.T) {
	sweepErr := errors.New("permission denied")
	uc := newMediaRetention(&fakeSweeper{err: sweepErr}, fakeOpenPayloads{}, 7*24*time.Hour, time.Now())
	if _, err := uc.Run(context.Background()); !errors.Is(err, sweepErr) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, sweepErr)
	}
}

func TestRunRetention_Media_SkippedWhenNotConfigured(t *testing.T) {
	sweeper := &fakeSweeper{}
	uc := newMediaRetention(sweeper, fakeOpenPayloads{}, 0, time.Now())
	if _, err := uc.Run(context.Background()); err != nil || sweeper.calls != 0 {
		t.Fatalf("Run() = %v, sweeps = %d; want no sweep without a retention", err, sweeper.calls)
	}
	plain := usecase.NewRunRetention(&fakeArticleRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeClusterRepo{}, &fakeLabelRepo{}, &fakeCheckpointer{}, in.RetentionWindows{}, fakeClock{now: time.Now()}, fakeLogger{})
	if _, err := plain.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
