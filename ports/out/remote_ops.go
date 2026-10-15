// Package out (this file): remote operations against the deployment host
// host running the service — pulling its live SQLite for training data
// (RemoteSync, this task) and, from Task 4 on, pushing a new deploy,
// checking its health, and rolling it back. Every interface here is a
// thin wrapper around an os/exec-invoked shell script (see
// a host-provided implementation) — no real SSH connection is made in any
// automated test.
package out

import (
	"context"
	"time"
)

// RemoteSyncRequest configures one remote sync script invocation.
type RemoteSyncRequest struct {
	Host          string
	RemoteDBHint  string
	LocalDBPath   string
	RetentionDays int
}

// RemoteSyncResult is what one successful Sync call hands back: the
// location and provenance of a validated, checksummed snapshot that has
// not yet been promoted into LocalDBPath — promotion is
// app/editorialsync.Run's job, not RemoteSync's.
type RemoteSyncResult struct {
	IncomingPath      string
	ChecksumSHA256    string
	SizeBytes         int64
	SourceHost        string
	SourcePath        string
	SnapshotCreatedAt time.Time
}

// RemoteSync pulls a consistent, integrity-checked snapshot of the
// remote host's operational SQLite database down to a temp file beside
// LocalDBPath and reports its location. It never touches LocalDBPath
// itself.
type RemoteSync interface {
	Sync(ctx context.Context, req RemoteSyncRequest) (RemoteSyncResult, error)
}

// RemoteDeployRequest configures one deploy.sh/rollback.sh invocation.
// APP_DIR on the remote host is not configurable per-request today — the
// scripts default to ~/phins-news-engine, matching this project's existing
// systemd WorkingDirectory convention; add a
// Host-keyed override here if a second remote target is ever needed.
type RemoteDeployRequest struct {
	Host, KeyPath                                                    string
	BinaryPath, ToolsBinaryPath, ConfigDir, ModelDir, DictionaryPath string
}

// RemoteDeploy uploads a new build to the remote host, backing up the
// current one first (see scripts/deploy.sh).
type RemoteDeploy interface {
	Deploy(ctx context.Context, req RemoteDeployRequest) error
}

// RemoteHealthCheckRequest configures one healthcheck.sh invocation.
type RemoteHealthCheckRequest struct{ Host, KeyPath string }

// RemoteHealthCheck reports whether the remote service is healthy.
// A non-nil error means the check itself could not run (e.g. SSH
// unreachable) — never means "unhealthy"; that is healthy=false, err=nil.
type RemoteHealthCheck interface {
	Check(ctx context.Context, req RemoteHealthCheckRequest) (healthy bool, detail string, err error)
}

// RemoteRollback restores the remote host's last-good backup (see
// scripts/rollback.sh) — a single slot, not a retained history.
type RemoteRollback interface {
	Rollback(ctx context.Context, req RemoteDeployRequest) error
}
