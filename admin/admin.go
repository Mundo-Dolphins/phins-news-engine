// Package admin is the composition root of the admin panel: it wires the
// action registry, the admin store, the job runner, authentication, the REST
// API, the MCP server and the embedded SPA into one process (ADR 0039).
//
// A host program (the bot's phins-news-admin binary) calls Run, after
// registering its own actions with Add.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/claims"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/train"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/httpapi"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/mcpserver"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/web"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

const (
	sessionTTL      = 12 * time.Hour
	loginMaxFails   = 5
	loginWindow     = time.Minute
	purgeInterval   = time.Hour
	shutdownTimeout = 10 * time.Second
)

// registry holds every action the admin offers. The engine's own actions are
// registered at init; hosts append theirs with Add.
var registry = &action.Registry{}

func init() {
	registry.Add(claims.Actions()...)
	registry.Add(train.Actions()...)
}

// Add registers host actions, like cli.Add does for commands. Call it before
// Run (typically from main or an init function); adding actions once Run has
// started is not supported. It panics on an invalid or duplicate action.
func Add(a ...action.Action) { registry.Add(a...) }

// Options configure Run.
type Options struct {
	// ConfigPath is the engine settings file.
	ConfigPath string
	// Profile is the --profile flag value; empty falls back to the profile
	// environment variable (config.ResolveProfile). "production" selects
	// production mode, anything else local mode.
	Profile string
	// Logger receives the admin's logs; nil means slog.Default().
	Logger *slog.Logger
	// Assets is the SPA build rooted at its dist directory; nil serves the
	// build embedded in this package's web assets.
	Assets fs.FS
	// Listeners, when set, replace the addresses in ADMIN_LISTEN_ADDRS
	// (used by tests). Run closes them on shutdown.
	Listeners []net.Listener
}

// Run starts the admin panel and blocks until ctx is done or a server fails.
// It returns nil after a clean shutdown. Shutdown order: stop the HTTP
// servers (no new jobs), cancel the running jobs, wait for them, then close
// the databases.
func Run(ctx context.Context, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}
	if err := cfg.Admin.Validate(); err != nil {
		return err
	}
	profile, err := config.ResolveProfile(opts.Profile)
	if err != nil {
		return err
	}
	mode := action.ModeLocal
	if profile == config.ProfileProduction {
		mode = action.ModeProduction
	}
	assets := opts.Assets
	if assets == nil {
		if assets, err = fs.Sub(web.Dist, "dist"); err != nil {
			return fmt.Errorf("admin: embedded assets: %w", err)
		}
	}

	opDB, err := infrasqlite.OpenImmediate(cfg.SQLite.Path, time.Duration(cfg.SQLite.BusyTimeoutMS)*time.Millisecond)
	if err != nil {
		return fmt.Errorf("admin: open operational DB: %w", err)
	}
	defer opDB.Close()

	var writesBlocked error
	if mode == action.ModeLocal {
		if err := infrasqlite.Migrate(ctx, opDB, migrations.FS); err != nil {
			return fmt.Errorf("admin: migrate operational DB: %w", err)
		}
	} else {
		writesBlocked = schemaGuard(ctx, cfg.SQLite.Path, opDB, log)
	}

	st, err := store.Open(ctx, cfg.Admin.DBPath)
	if err != nil {
		return fmt.Errorf("admin: open admin DB: %w", err)
	}
	defer st.Close()

	listeners := opts.Listeners
	if len(listeners) == 0 {
		if listeners, err = listenAll(cfg.Admin.ListenAddrs); err != nil {
			return err
		}
	}

	now := func() time.Time { return time.Now().UTC() }
	jobsCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	runner, err := jobs.New(jobsCtx, st, now)
	if err != nil {
		closeAll(listeners)
		return fmt.Errorf("admin: job runner: %w", err)
	}

	envFor := func(actor string) action.Env {
		return action.Env{DB: opDB, Config: cfg, Mode: mode, Now: now, Log: log, Actor: actor}
	}
	handler := httpapi.NewHandler(httpapi.Deps{
		Registry: registry, Runner: runner, Store: st,
		Sessions: auth.NewSessions(st, sessionTTL, now),
		Limiter:  auth.NewLoginLimiter(loginMaxFails, loginWindow, now),
		EnvFor:   envFor, Username: cfg.Admin.Username, PasswordHash: cfg.Admin.PasswordHash,
		WritesBlocked: writesBlocked, Assets: assets, Mode: mode,
		MCP: mcpserver.New(registry, mode, runner, st, envFor, writesBlocked),
	}, cfg.Admin.AllowedCIDRs, cfg.Admin.MCPToken)

	// streamCtx ends open SSE streams when shutdown begins.
	streamCtx, endStreams := context.WithCancel(context.Background())
	defer endStreams()
	servers := make([]*http.Server, len(listeners))
	serveErrs := make(chan error, len(listeners))
	for i, ln := range listeners {
		servers[i] = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second, // no WriteTimeout: SSE streams are long-lived
			BaseContext:       func(net.Listener) context.Context { return streamCtx },
		}
		go func(srv *http.Server, ln net.Listener) {
			log.Info("admin listening", "addr", ln.Addr().String(), "mode", string(mode))
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErrs <- err
			}
		}(servers[i], ln)
	}

	purgeCtx, stopPurge := context.WithCancel(context.Background())
	purged := make(chan struct{})
	go func() {
		defer close(purged)
		purgeSessions(purgeCtx, st, now, log)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErrs:
		log.Error("admin server failed", "error", runErr)
	}

	endStreams()
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Warn("admin server shutdown", "error", err)
			_ = srv.Close()
		}
	}
	cancelJobs()
	runner.Wait()
	stopPurge()
	<-purged
	return runErr
}

// schemaGuard compares the operational DB with this binary's migrations
// without changing it. When the DB is ahead of the binary, behind it, or
// cannot be checked, it returns the error that blocks write and destructive
// actions (reads keep working); otherwise nil.
func schemaGuard(ctx context.Context, path string, db *sql.DB, log *slog.Logger) error {
	var problems []string
	if err := infrasqlite.CheckSchemaCompatible(ctx, path, migrations.FS); err != nil {
		problems = append(problems, err.Error())
	}
	pending, err := infrasqlite.PendingMigrations(ctx, db, migrations.FS)
	if err != nil {
		problems = append(problems, err.Error())
	} else if len(pending) > 0 {
		problems = append(problems, "pending migrations: "+strings.Join(pending, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	blocked := &action.Error{
		Code:    action.CodeSchemaMismatch,
		Message: "operational DB schema differs from this binary: " + strings.Join(problems, "; "),
	}
	log.Warn("admin: write actions disabled", "reason", blocked.Message)
	return blocked
}

func listenAll(addrs []string) ([]net.Listener, error) {
	var lns []net.Listener
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			closeAll(lns)
			return nil, fmt.Errorf("admin: listen %s: %w", addr, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

// purgeSessions drops expired admin sessions every purgeInterval until ctx ends.
func purgeSessions(ctx context.Context, st *store.Store, now func() time.Time, log *slog.Logger) {
	t := time.NewTicker(purgeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := st.PurgeExpiredSessions(ctx, now()); err != nil && ctx.Err() == nil {
				log.Warn("admin: purge expired sessions", "error", err)
			}
		}
	}
}
