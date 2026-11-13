package editorialcycle_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestAbandonCancelsSession(t *testing.T) {
	env := newTestEnv(t, "a1", "a2")
	session := env.start(t, "a1", "a2")
	if err := editorialcycle.Abandon(context.Background(), env.deps, session); err != nil {
		t.Fatalf("Abandon() error = %v", err)
	}
	if got := env.status(t, session.ID); got != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got)
	}
	// A cancelled session is not resumable: the next start is a fresh session.
	env.advance(1)
	next, resumed, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{SampleSize: 5}, env.candidatesFor("a1"))
	if err != nil || resumed || next.ID == session.ID {
		t.Fatalf("StartOrResume after Abandon = %s resumed=%v err=%v, want a new session", next.ID, resumed, err)
	}
}

func TestSelectFreshMatchesCLI(t *testing.T) {
	env := newTestEnv(t, "a1", "a2", "a3", "a4", "a5")
	env.deps.ExcludedSourceIDs = map[string]bool{"rss:excluded": true}
	cfg := &config.Config{}
	cfg.EditorialCycle.SampleSize = 3
	cfg.EditorialCycle.PoolLimit = 4
	cfg.EditorialCycle.AmbiguousCooldownDays = 14

	alDeps := activelearning.Deps{
		Articles: env.deps.Articles, Labels: env.deps.Labels, Events: env.deps.Events,
		ReviewDecisions: env.deps.ReviewDecisions, Clock: env.deps.Clock, ExcludedSourceIDs: env.deps.ExcludedSourceIDs,
	}
	for _, flag := range []int{0, 2} {
		resolved := flag
		if resolved <= 0 {
			resolved = cfg.EditorialCycle.SampleSize
		}
		want, err := activelearning.Select(context.Background(), alDeps, activelearning.Options{
			Since: env.now.AddDate(0, 0, -30), PoolLimit: cfg.EditorialCycle.PoolLimit,
			SampleSize: resolved, AmbiguousCooldown: cfg.EditorialCycle.AmbiguousCooldown(),
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := editorialcycle.SelectFresh(context.Background(), env.deps, cfg, flag)
		if err != nil {
			t.Fatalf("SelectFresh(%d) error = %v", flag, err)
		}
		if !reflect.DeepEqual(got, want) || len(got) != resolved {
			t.Fatalf("SelectFresh(%d) = %+v, want %+v", flag, got, want)
		}
	}
}

func TestExcludedSourceIDsJoinsBypassAndUnconditional(t *testing.T) {
	cfg := &config.Config{}
	cfg.Editorial.ModelBypassSourceIDs = []string{"rss:a"}
	cfg.Editorial.UnconditionalPublishSourceIDs = []string{"rss:b"}
	got := editorialcycle.ExcludedSourceIDs(cfg)
	if want := map[string]bool{"rss:a": true, "rss:b": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExcludedSourceIDs = %v, want %v", got, want)
	}
}
