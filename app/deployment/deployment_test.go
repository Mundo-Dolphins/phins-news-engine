package deployment_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/deployment"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeRunner struct {
	responses map[string]struct {
		stdout string
		err    error
	}
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.calls = append(f.calls, key)
	if r, ok := f.responses[key]; ok {
		return r.stdout, r.err
	}
	return "", nil
}

const sanitizedCICommand = "env -u TELEGRAM_BOT_TOKEN -u TELEGRAM_API_ID -u TELEGRAM_API_HASH -u TELEGRAM_PHONE -u TELEGRAM_SESSION_PATH -u TELEGRAM_TIMEOUT_SECONDS -u SCRAPING_SERVICE_API_KEY -u SQLITE_DB_PATH -u NEWS_ENGINE_PROFILE make ci"

type fakeDeploy struct {
	deployed bool
	err      error
}

func (f *fakeDeploy) Deploy(context.Context, out.RemoteDeployRequest) error {
	f.deployed = true
	return f.err
}

type fakeHealth struct {
	healthy bool
	detail  string
	err     error
}

func (f *fakeHealth) Check(context.Context, out.RemoteHealthCheckRequest) (bool, string, error) {
	return f.healthy, f.detail, f.err
}

type fakeRollback struct {
	called bool
	err    error
}

func (f *fakeRollback) Rollback(context.Context, out.RemoteDeployRequest) error {
	f.called = true
	return f.err
}

func TestRun_HappyPath_DeploysAndVerifies(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"ok", nil},
	}}
	deploy := &fakeDeploy{}
	health := &fakeHealth{healthy: true, detail: "HEALTHY"}
	rollback := &fakeRollback{}

	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: deploy, Health: health, Rollback: rollback},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.CIPassed || !result.Built || !result.Deployed || !result.Healthy {
		t.Errorf("result = %+v, want a fully successful run", result)
	}
	if !deploy.deployed || rollback.called {
		t.Errorf("deploy.deployed=%v rollback.called=%v, want deployed and not rolled back", deploy.deployed, rollback.called)
	}
}

func TestRun_FailedCIAbortsBeforeBuildOrDeploy(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"FAIL", errors.New("ci failed")},
	}}
	deploy := &fakeDeploy{}
	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: deploy, Health: &fakeHealth{}, Rollback: &fakeRollback{}},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err == nil {
		t.Fatal("Run() error = nil, want an error when CI fails")
	}
	if result.CIPassed || result.Built || deploy.deployed {
		t.Errorf("result = %+v, deploy.deployed=%v, want nothing built or deployed after a CI failure", result, deploy.deployed)
	}
}

func TestRun_FailedHealthCheckTriggersRollback(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"ok", nil},
	}}
	rollback := &fakeRollback{}
	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: &fakeDeploy{}, Health: &fakeHealth{healthy: false, detail: "UNHEALTHY"}, Rollback: rollback},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err == nil {
		t.Fatal("Run() error = nil, want an error surfaced when a failed health check triggers rollback")
	}
	if !rollback.called {
		t.Error("rollback.called = false, want true after a failed health check")
	}
	if !result.RolledBack {
		t.Errorf("result.RolledBack = false, want true")
	}
}

func TestRun_ForceSkipsAutomaticRollback(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"ok", nil},
	}}
	rollback := &fakeRollback{}
	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: &fakeDeploy{}, Health: &fakeHealth{healthy: false, detail: "UNHEALTHY"}, Rollback: rollback},
		deployment.Request{Host: "h", CITimeout: time.Minute, Force: true})
	if err == nil {
		t.Fatal("Run() error = nil, want the unhealthy result still surfaced as an error even with --force")
	}
	if rollback.called {
		t.Error("rollback.called = true, want false — --force means \"don't auto-rollback\"")
	}
	if result.Healthy {
		t.Error("result.Healthy = true, want false")
	}
}

func TestRun_FailedRollbackIsReportedNotSwallowed(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"ok", nil},
	}}
	rollback := &fakeRollback{err: errors.New("no backup found")}
	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: &fakeDeploy{}, Health: &fakeHealth{healthy: false}, Rollback: rollback},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err == nil {
		t.Fatal("Run() error = nil, want the rollback failure surfaced")
	}
	if result.RollbackErr == nil {
		t.Error("result.RollbackErr = nil, want the rollback's own error recorded")
	}
}

func TestRun_DirtyWorkingTreeBlocksDeploy(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {" M some/file.go\n", nil},
	}}
	deploy := &fakeDeploy{}
	result, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: deploy, Health: &fakeHealth{}, Rollback: &fakeRollback{}},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err == nil {
		t.Fatal("Run() error = nil, want an error when the working tree is dirty")
	}
	if result.GitClean || deploy.deployed {
		t.Errorf("result.GitClean=%v deploy.deployed=%v, want gate to block before CI/deploy", result.GitClean, deploy.deployed)
	}
}

func TestRun_CIRunsWithRuntimeTelegramEnvUnset(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"ok", nil},
	}}

	_, err := deployment.Run(context.Background(), deployment.Deps{
		Runner: runner, Deploy: &fakeDeploy{}, Health: &fakeHealth{healthy: true}, Rollback: &fakeRollback{},
	}, deployment.Request{Host: "h", CITimeout: time.Minute})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !slices.Contains(runner.calls, sanitizedCICommand) {
		t.Fatalf("calls = %v, want sanitized CI command", runner.calls)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "make ci") {
			t.Fatalf("calls = %v, CI must not inherit runtime Telegram env", runner.calls)
		}
	}
}

// Exercise Git's real pathspec handling for staged, unstaged and untracked
// artifacts, while replacing CI/build and the remote operations with fakes.
type gitRunner struct {
	dir   string
	calls []string
}

func (r *gitRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name)
	if name != "git" {
		return "ok", nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.dir
	cmd.Env = gitEnv()
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// gitEnv is the environment for git commands run against a throwaway
// repository: the caller's own, minus every GIT_* variable. Git exports
// GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE to hooks (the pre-push hook runs
// `make ci`, hence these tests); left in place they override cmd.Dir, so
// `git init` and `git commit` would act on the real repository instead.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

// simulateGitHookEnv points the hook-exported variables at decoys under
// temp directories, so a test that forgets gitEnv fails here instead of
// touching the real repository.
func simulateGitHookEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "decoy.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "decoy-index"))
}

func TestRun_EditorialArtifactsDoNotBlockButOtherChangesDo(t *testing.T) {
	simulateGitHookEnv(t)
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv()
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	write("configs/dictionary.yml", "old")
	write("models/current/model.json", "old")
	write("models/candidates/model.json", "old")
	write("configs/settings.yml", "old")
	write("main.go", "old")
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	write("configs/dictionary.yml", "new")
	write("models/current/model.json", "new")
	write("models/candidates/model.json", "new")
	git("add", "models/candidates")
	write("models/previous/model.json", "backup")
	req := deployment.Request{Host: "h", AllowedDirtyPaths: []string{
		"configs/dictionary.yml", "models/current", "models/candidates", "models/previous",
	}}
	for _, path := range []string{"", "main.go", "configs/settings.yml", "models/unrelated/model.json"} {
		t.Run(path, func(t *testing.T) {
			if path != "" {
				write(path, "new")
			}
			runner := &gitRunner{dir: dir}
			deploy := &fakeDeploy{}
			result, err := deployment.Run(context.Background(), deployment.Deps{
				Runner: runner, Deploy: deploy, Health: &fakeHealth{healthy: true}, Rollback: &fakeRollback{},
			}, req)
			if path == "" {
				if err != nil || !deploy.deployed || result.GitClean || !result.PreDeployGatesPassed {
					t.Fatalf("result=%+v deployed=%v err=%v", result, deploy.deployed, err)
				}
			} else {
				if err == nil || deploy.deployed || result.CIPassed || !strings.Contains(err.Error(), strings.TrimSuffix(path, "model.json")) {
					t.Fatalf("result=%+v deployed=%v err=%v", result, deploy.deployed, err)
				}
				if path == "models/unrelated/model.json" {
					os.Remove(filepath.Join(dir, path))
				} else {
					git("checkout", "--", path)
				}
			}
		})
	}
}

func TestRun_RejectsBroadArtifactPaths(t *testing.T) {
	for _, path := range []string{".", "..", "../outside"} {
		t.Run(path, func(t *testing.T) {
			runner := &fakeRunner{responses: map[string]struct {
				stdout string
				err    error
			}{
				"git status --porcelain": {" M main.go", nil},
			}}
			_, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner}, deployment.Request{AllowedDirtyPaths: []string{path}})
			if err == nil || !strings.Contains(err.Error(), "artifact path must be within") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRun_BuildFailureCarriesTheBuildOutput(t *testing.T) {
	runner := &fakeRunner{responses: map[string]struct {
		stdout string
		err    error
	}{
		"git status --porcelain": {"", nil},
		sanitizedCICommand:       {"ok", nil},
		"make build-deploy":      {"# example/cmd\ncmd/main.go:9:2: undefined: nope", errors.New("exit status 2")},
	}}
	_, err := deployment.Run(context.Background(), deployment.Deps{Runner: runner, Deploy: &fakeDeploy{}, Health: &fakeHealth{}, Rollback: &fakeRollback{}},
		deployment.Request{Host: "h", CITimeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "undefined: nope") {
		t.Fatalf("Run() error = %v, want it to carry the build output", err)
	}
}
