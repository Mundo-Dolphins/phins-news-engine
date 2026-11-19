package engine

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/logging"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// wiringHandler asks for one task per article it sees.
type wiringHandler struct{ name string }

func (h wiringHandler) Name() string                  { return h.name }
func (wiringHandler) Interested(article.Article) bool { return true }
func (wiringHandler) Handle(context.Context, out.EventContext, article.Article) (out.EventResult, error) {
	return out.EventResult{Actions: []out.EventAction{out.EnqueueTask{Kind: "poll", Key: "k1"}}}, nil
}

type wiringRunner struct{ kind string }

func (r wiringRunner) Kind() string { return r.kind }
func (wiringRunner) Run(context.Context, out.TaskContext, out.Task, []out.TaskSignal) (out.TaskOutcome, error) {
	return out.TaskOutcome{Status: out.TaskDone}, nil
}

func TestArticleHandlerSettingsKeepsSharedBlocksAndDropsRunnerOnlyOnes(t *testing.T) {
	settings := map[string]usecase.EventHandlerSettings{
		"shared": {Enabled: true}, "runner-only": {Enabled: true}, "other": {Enabled: true},
	}
	got := articleHandlerSettings(settings, Options{
		EventHandlers: []out.EventHandler{wiringHandler{"shared"}, wiringHandler{"other"}},
		TaskRunners:   []out.TaskRunner{wiringRunner{"shared"}, wiringRunner{"runner-only"}},
	})
	if _, ok := got["shared"]; !ok {
		t.Error("the block shared with an article handler was dropped")
	}
	if _, ok := got["runner-only"]; ok {
		t.Error("a runner-only block would make the dispatcher warn about an unregistered handler")
	}
	if _, ok := got["other"]; !ok {
		t.Error("an unrelated handler block was dropped")
	}
	if len(settings) != 3 {
		t.Error("the caller's map was modified")
	}
}

// runEngine boots Run into the scheduler with a handler that enqueues a task
// for the first article, runs check against the database once that task
// exists, and stops Run.
func runEngine(t *testing.T, opts Options, extraSettings string, check func(db *sql.DB)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(testRSSFeedXML))
	}))
	defer server.Close()
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "disabled", "")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "cleanup_interval_hours:", "task_interval_seconds: 1\n  cleanup_interval_hours:", 1))
	extra := "\nevents:\n  poll:\n    enabled: true\n" + extraSettings
	if err := os.WriteFile(cfgPath, append(raw, []byte(extra)...), 0o600); err != nil {
		t.Fatal(err)
	}
	sourcesPath := filepath.Join(dir, "sources.yml")
	sources, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcesPath, []byte(strings.Replace(string(sources), "http://127.0.0.1:1", server.URL, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfgPath, config.ProfileTest, logging.New("error", false), opts) }()

	db, err := sql.Open("sqlite", filepath.Join(dir, "operational.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(10 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err == nil && count > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if count == 0 {
		t.Fatal("no task was enqueued: EnqueueTask is not wired into the pipeline")
	}
	check(db)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}
}

func taskStatus(db *sql.DB) string {
	var s string
	_ = db.QueryRow(`SELECT status FROM tasks WHERE kind = 'poll' AND task_key = 'k1'`).Scan(&s)
	return s
}

func TestRunWithoutTaskRunnersStartsNoTaskLoop(t *testing.T) {
	runEngine(t, Options{EventHandlers: []out.EventHandler{wiringHandler{"poll"}}}, "", func(db *sql.DB) {
		time.Sleep(2500 * time.Millisecond)
		if got := taskStatus(db); got != "pending" {
			t.Fatalf("task status = %q, want it untouched without runners", got)
		}
	})
}

func TestRunWithEnabledTaskRunnerAdvancesTasks(t *testing.T) {
	opts := Options{
		EventHandlers: []out.EventHandler{wiringHandler{"poll"}},
		TaskRunners:   []out.TaskRunner{wiringRunner{"poll"}},
	}
	runEngine(t, opts, "", func(db *sql.DB) {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) && taskStatus(db) != "done" {
			time.Sleep(100 * time.Millisecond)
		}
		if got := taskStatus(db); got != "done" {
			t.Fatalf("task status = %q, want done", got)
		}
	})
}
