// Package deployment orchestrates CI, build, pre-deploy gates, deploy,
// health verification, and conditional rollback — extracted as a library
// so both the standalone `deploy` command and `editorial cycle`'s tail
// call identical logic. The editorial cycle allows its generated artifacts to
// be dirty; other changes block deployment. Rollback is automatic unless Force.
package deployment

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// CommandRunner abstracts os/exec so this package is testable without a
// real subprocess. shellrunner.Real{} is the production implementation.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

// Deps are the ports Run writes through.
type Deps struct {
	Runner   CommandRunner
	Deploy   out.RemoteDeploy
	Health   out.RemoteHealthCheck
	Rollback out.RemoteRollback
}

// Request configures one Run call.
type Request struct {
	Host, KeyPath                                                    string
	BinaryPath, ToolsBinaryPath, ConfigDir, ModelDir, DictionaryPath string
	CITimeout                                                        time.Duration
	Force                                                            bool // skip automatic rollback on a failed health check
	// AllowedDirtyPaths contains generated artifacts that may change during an
	// editorial cycle. Standalone deployments require a completely clean tree.
	AllowedDirtyPaths []string
}

// Result is the full outcome of one Run call, assembled incrementally —
// editorial cycle's AuditReport (a later task) embeds this directly.
type Result struct {
	GitClean             bool
	PreDeployGatesPassed bool
	PreDeployGateFailure string
	CIOutput             string
	CIPassed             bool
	Built                bool
	Deployed             bool
	Healthy              bool
	HealthDetail         string
	RolledBack           bool
	RollbackErr          error
}

var ciEnvUnsetArgs = []string{
	"-u", "TELEGRAM_BOT_TOKEN",
	"-u", "TELEGRAM_API_ID",
	"-u", "TELEGRAM_API_HASH",
	"-u", "TELEGRAM_PHONE",
	"-u", "TELEGRAM_SESSION_PATH",
	"-u", "TELEGRAM_TIMEOUT_SECONDS",
	"-u", "SCRAPING_SERVICE_API_KEY",
	"-u", "SQLITE_DB_PATH",
}

// Run executes the pre-deploy gate, then CI, build, deploy, verify, and
// (on a failed health check, unless req.Force) rollback, in order,
// stopping at the first failure. A failed rollback is recorded in
// Result.RollbackErr and also returned as part of the top-level error —
// never silently swallowed.
func Run(ctx context.Context, deps Deps, req Request) (Result, error) {
	var result Result

	status, err := deps.Runner.Run(ctx, "git", "status", "--porcelain")
	if err != nil {
		return result, fmt.Errorf("deployment: git status: %w", err)
	}
	result.GitClean = strings.TrimSpace(status) == ""
	if !result.GitClean {
		blocked := status
		if len(req.AllowedDirtyPaths) > 0 {
			args := []string{"status", "--porcelain", "--", "."}
			for _, path := range req.AllowedDirtyPaths {
				if path == "" {
					continue
				}
				abs, err := filepath.Abs(path)
				if err != nil {
					return result, fmt.Errorf("deployment: artifact path: %w", err)
				}
				cwd, err := filepath.Abs(".")
				if err != nil {
					return result, fmt.Errorf("deployment: working directory: %w", err)
				}
				rel, err := filepath.Rel(cwd, abs)
				if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return result, fmt.Errorf("deployment: artifact path must be within working directory: %q", path)
				}
				args = append(args, ":(exclude,literal)"+filepath.ToSlash(rel))
			}
			blocked, err = deps.Runner.Run(ctx, "git", args...)
			if err != nil {
				return result, fmt.Errorf("deployment: git status excluding generated artifacts: %w", err)
			}
		}
		if strings.TrimSpace(blocked) != "" {
			result.PreDeployGateFailure = "working tree is not clean"
			return result, fmt.Errorf("deployment: pre-deploy gate failed: %s\n%s", result.PreDeployGateFailure, strings.TrimSpace(blocked))
		}
	}
	result.PreDeployGatesPassed = true

	ciCtx := ctx
	var cancel context.CancelFunc
	if req.CITimeout > 0 {
		ciCtx, cancel = context.WithTimeout(ctx, req.CITimeout)
		defer cancel()
	}
	ciArgs := append(append([]string{}, ciEnvUnsetArgs...), "-u", config.ProfileEnvVar, "make", "ci")
	ciOut, err := deps.Runner.Run(ciCtx, "env", ciArgs...)
	result.CIOutput = ciOut
	if err != nil {
		return result, fmt.Errorf("deployment: make ci failed: %w", err)
	}
	result.CIPassed = true

	if buildOut, err := deps.Runner.Run(ctx, "make", "build-deploy"); err != nil {
		return result, fmt.Errorf("deployment: make build-deploy failed: %w%s", err, outputTail(buildOut))
	}
	result.Built = true

	deployReq := out.RemoteDeployRequest{
		Host: req.Host, KeyPath: req.KeyPath, BinaryPath: req.BinaryPath, ToolsBinaryPath: req.ToolsBinaryPath,
		ConfigDir: req.ConfigDir, ModelDir: req.ModelDir, DictionaryPath: req.DictionaryPath,
	}
	if err := deps.Deploy.Deploy(ctx, deployReq); err != nil {
		return result, fmt.Errorf("deployment: deploy failed: %w", err)
	}
	result.Deployed = true

	healthy, detail, err := deps.Health.Check(ctx, out.RemoteHealthCheckRequest{Host: req.Host, KeyPath: req.KeyPath})
	if err != nil {
		return result, fmt.Errorf("deployment: health check failed to run: %w", err)
	}
	result.Healthy, result.HealthDetail = healthy, detail
	if healthy {
		return result, nil
	}

	if req.Force {
		return result, fmt.Errorf("deployment: deploy is unhealthy (%s) — not rolling back due to --force", detail)
	}

	if err := deps.Rollback.Rollback(ctx, deployReq); err != nil {
		result.RollbackErr = err
		return result, fmt.Errorf("deployment: deploy unhealthy (%s) AND rollback failed: %w", detail, err)
	}
	result.RolledBack = true
	return result, fmt.Errorf("deployment: deploy was unhealthy (%s) — rolled back to the previous version", detail)
}

// maxOutputTail bounds how much command output an error carries.
const maxOutputTail = 4000

// outputTail formats the end of a failed command's combined output for an
// error message ("" when there is none). The cause of a failed build is in
// that output; the exit status alone ("exit status 2") hides it.
func outputTail(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if len(output) > maxOutputTail {
		output = "...\n" + output[len(output)-maxOutputTail:]
	}
	return "\n--- command output ---\n" + output
}
