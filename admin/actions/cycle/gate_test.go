package cycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/cycle"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// blockingSync holds the sync until release is closed, then fails it.
type blockingSync struct{ started, release chan struct{} }

func (s blockingSync) Sync(context.Context, out.RemoteSyncRequest) (out.RemoteSyncResult, error) {
	close(s.started)
	<-s.release
	return out.RemoteSyncResult{}, errors.New("remote unreachable")
}

func cycleAction(t *testing.T, name string) action.Action {
	t.Helper()
	for _, a := range cycle.Actions() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no action %q", name)
	return action.Action{}
}

// F6: cycle.state reads only the admin store, so the wizard can follow a
// running sync: it answers while cycle.sync swaps the database.
func TestStateDuringSync(t *testing.T) {
	f := newFixture(t)
	bs := blockingSync{started: make(chan struct{}), release: make(chan struct{})}
	f.remote = action.Remote{Host: "box.example", NewSync: func(string, string) out.RemoteSync { return bs }}
	f.mustRun("cycle.start", `{}`)
	r, err := jobs.New(context.Background(), f.st, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	r.UseDB(f.db)
	t.Cleanup(func() {
		if db := r.DB(); db != nil {
			_ = db.Close()
		}
	})
	if _, err := r.Start(cycleAction(t, "cycle.sync"), f.env(), nil, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	<-bs.started
	res, err := r.RunSync(context.Background(), cycleAction(t, "cycle.state"), f.env(), nil, nil)
	close(bs.release)
	r.Wait()
	if err != nil {
		t.Fatalf("cycle.state during the sync: %v", err)
	}
	v := decode[view](t, res)
	if v.Cycle == nil || v.Cycle.Step != "sync" || v.Cycle.Steps["sync"].Status != "running" {
		t.Errorf("state during sync = %+v", v.Cycle)
	}
}
