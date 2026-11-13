package jobs

import (
	"database/sql"
	"log/slog"
	"slices"
	"sync"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
)

// ErrDBSwapInProgress is returned for any action requested while an action
// with SwapsDB is replacing the operational DB.
var ErrDBSwapInProgress = &action.Error{Code: action.CodeConflict, Message: "database sync in progress"}

// ErrActionsRunning is returned when an action with SwapsDB is requested
// while other actions or jobs are running.
var ErrActionsRunning = &action.Error{Code: action.CodeConflict, Message: "other actions are running; try again when they finish"}

// dbGate hands out the operational DB. Every run holds it shared; an action
// with SwapsDB holds it exclusively. Neither waits: a taken gate fails fast.
type dbGate struct {
	mu       sync.Mutex
	shared   int
	swapping bool
	// swapGen numbers swaps, so a SwapDB kept from an earlier swap is inert.
	swapGen uint64
	db      *sql.DB
	// retired are the handles replaced during the current swap, closed once
	// the swapping action returns.
	retired []*sql.DB
}

// enter takes the gate for a and points env at the current DB (when one is
// set), so an env built before a swap never keeps a stale handle. The
// returned leave releases it. An action with NoOperationalDB bypasses the
// gate: it gets no DB and neither waits for nor blocks a swap.
func (g *dbGate) enter(a action.Action, env *action.Env) (leave func(), err error) {
	if a.NoOperationalDB {
		env.DB, env.SwapDB = nil, nil
		return func() {}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.swapping {
		return nil, ErrDBSwapInProgress
	}
	if g.db != nil {
		env.DB = g.db
	}
	env.SwapDB = nil
	if !a.SwapsDB {
		g.shared++
		return g.leaveShared, nil
	}
	if g.shared > 0 {
		return nil, ErrActionsRunning
	}
	g.swapping = true
	g.swapGen++
	gen := g.swapGen
	env.SwapDB = func(db *sql.DB) { g.swap(gen, db) }
	return g.leaveSwap, nil
}

func (g *dbGate) leaveShared() {
	g.mu.Lock()
	g.shared--
	g.mu.Unlock()
}

// swap installs db as the current handle; the replaced one is retired. It
// has no effect for a nil db or once swap gen is over (the SwapDB was kept
// past its action), so replaced handles are only closed by leaveSwap.
func (g *dbGate) swap(gen uint64, db *sql.DB) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if db == nil || !g.swapping || gen != g.swapGen {
		return
	}
	if g.db != nil && g.db != db && !slices.Contains(g.retired, g.db) {
		g.retired = append(g.retired, g.db)
	}
	g.db = db
}

// leaveSwap closes the handles the swap replaced, then reopens the gate.
func (g *dbGate) leaveSwap() {
	g.mu.Lock()
	retired, current := g.retired, g.db
	g.retired = nil
	g.mu.Unlock()
	for _, db := range retired {
		if db == current {
			continue
		}
		if err := db.Close(); err != nil {
			slog.Warn("admin: close replaced operational DB", "err", err)
		}
	}
	g.mu.Lock()
	g.swapping = false
	g.mu.Unlock()
}

// current returns the operational DB in use.
func (g *dbGate) current() *sql.DB {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.db
}

// set installs db without closing the previous handle.
func (g *dbGate) set(db *sql.DB) {
	g.mu.Lock()
	g.db = db
	g.mu.Unlock()
}
