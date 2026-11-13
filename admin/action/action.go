package action

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Kind classifies the side effects of an action.
type Kind string

// Action kinds.
const (
	KindRead        Kind = "read"
	KindWrite       Kind = "write"
	KindDestructive Kind = "destructive"
)

// Mode is the deployment mode the admin runs in.
type Mode string

// Admin modes.
const (
	ModeLocal      Mode = "local"
	ModeProduction Mode = "production"
)

// Env is what an action receives to do its work.
type Env struct {
	DB     *sql.DB
	Config *config.Config
	Mode   Mode
	Now    func() time.Time
	// Log is job/request scoped; its lines end up in the job log.
	Log *slog.Logger
	// Actor is "user:<name>" or "mcp".
	Actor string
	// StatsLogPath is the process log the host wants stats.report to scan
	// when the caller gives no log_path.
	StatsLogPath string
	// ConfigPath is the settings file the admin loaded.
	ConfigPath string
	// Profile is the resolved profile (local or production).
	Profile config.Profile
	// FetcherExtensions are the host's fetcher categories, as the CLI
	// receives them, for actions that build the process fetcher.
	FetcherExtensions fetcherwiring.Extensions
	// SwapDB is non-nil only while an action with SwapsDB runs: it installs
	// newDB as the operational DB for every later action (web and MCP) and
	// closes the replaced handle once the swapping action returns.
	SwapDB func(newDB *sql.DB)
	// State is the admin's own key/value state; it lives in the admin store,
	// so it survives a swap of the operational DB.
	State StateStore
	// Remote is the host's remote-sync and Telegram hooks; its zero value
	// means they are not available on this host.
	Remote Remote
}

// StateStore is a small persistent key/value store.
type StateStore interface {
	// GetState returns the value stored under key and whether it exists.
	GetState(ctx context.Context, key string) ([]byte, bool, error)
	// PutState stores value under key, replacing any previous value.
	PutState(ctx context.Context, key string, value []byte) error
}

// Remote holds the host-provided hooks to reach the remote host and
// Telegram. Host and KeyPath never come from a panel field.
type Remote struct {
	// Host is the remote host address; KeyPath the SSH key used to reach it.
	Host, KeyPath string
	// NewSync builds the remote sync for the given script and key.
	NewSync func(scriptPath, keyPath string) out.RemoteSync
	// NewTelegramClient builds the Telegram channel history client.
	NewTelegramClient func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error)
}

// Action is one admin operation.
type Action struct {
	Name, Group, Title, Description string
	Kind                            Kind
	Modes                           []Mode
	LongRunning, Exclusive          bool
	// SwapsDB marks an action that may replace the operational DB (through
	// Env.SwapDB); it runs only when no other action or job is running.
	SwapsDB bool
	// Params is the zero value of the params struct, used for the schema.
	// It may be nil for actions without parameters.
	Params any
	// Run executes the action; params is a pointer to a decoded copy.
	Run func(ctx context.Context, env Env, params any) (Result, error)
}

// Registry holds actions in registration order.
type Registry struct {
	actions []Action
	byName  map[string]int
}

// Add registers actions. It panics on an empty name, no modes, a nil Run,
// a duplicate name or an invalid Params struct.
func (r *Registry) Add(as ...Action) {
	for _, a := range as {
		switch {
		case a.Name == "":
			panic("action: empty name")
		case len(a.Modes) == 0:
			panic(fmt.Sprintf("action %q: no modes", a.Name))
		case a.Run == nil:
			panic(fmt.Sprintf("action %q: nil Run", a.Name))
		}
		if _, dup := r.byName[a.Name]; dup {
			panic(fmt.Sprintf("action %q: duplicate", a.Name))
		}
		if _, err := paramFields(a.Params); err != nil {
			panic(fmt.Sprintf("action %q: %v", a.Name, err))
		}
		if r.byName == nil {
			r.byName = map[string]int{}
		}
		r.byName[a.Name] = len(r.actions)
		r.actions = append(r.actions, a)
	}
}

// Get returns the action with the given name.
func (r *Registry) Get(name string) (Action, bool) {
	i, ok := r.byName[name]
	if !ok {
		return Action{}, false
	}
	return r.actions[i], true
}

// For returns the actions available in mode, sorted by Group then Name.
func (r *Registry) For(mode Mode) []Action {
	var out []Action
	for _, a := range r.actions {
		for _, m := range a.Modes {
			if m == mode {
				out = append(out, a)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}
