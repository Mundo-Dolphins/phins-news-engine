package stats_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/stats"
	"github.com/Mundo-Dolphins/phins-news-engine/app/pipelinestats"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func run(t *testing.T, logPath, params string) (action.Result, error) {
	t.Helper()
	cfg, err := config.Load(testsupport.WriteFixture(t))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	var db *sql.DB
	if db, err = infrasqlite.Open(filepath.Join(t.TempDir(), "op.db"), 5000); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a := stats.Actions()[0]
	p, err := action.Decode(a, json.RawMessage(params))
	if err != nil {
		return action.Result{}, err
	}
	env := action.Env{DB: db, Config: cfg, Mode: action.ModeProduction, Now: func() time.Time { return now },
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), Actor: "mcp", StatsLogPath: logPath}
	return a.Run(context.Background(), env, p)
}

func TestStatsReportUsesHostLogPath(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "out.log")
	line := `{"level":"info","fetched":4,"time":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","message":"process cycle completed"}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, logPath, `{}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rep, ok := res.Object.(pipelinestats.Report)
	if !ok || res.Render != "object" {
		t.Fatalf("result = %+v, want object Report", res)
	}
	if rep.LogCyclesRun != 1 || rep.LogPath != logPath {
		t.Errorf("report = %+v, want 1 cycle from the host log path", rep)
	}
	if !strings.HasPrefix(res.Summary, "analyzed=0 accepted=0 rejected=0 reject_rate=0.000") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestStatsBadSince(t *testing.T) {
	_, err := run(t, "", `{"since":"24"}`)
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid params", err)
	}
}

func TestStatsMetadata(t *testing.T) {
	as := stats.Actions()
	if len(as) != 1 || as[0].Name != "stats.report" || as[0].Kind != action.KindRead || len(as[0].Modes) != 2 {
		t.Fatalf("actions = %+v", as)
	}
}

func TestStatsProductionRejectsOtherLogPath(t *testing.T) {
	hostLog := filepath.Join(t.TempDir(), "host.log")
	_, err := run(t, hostLog, `{"log_path":"`+filepath.Join(t.TempDir(), "other.log")+`"}`)
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams || !strings.Contains(ae.Message, "log_path") {
		t.Fatalf("err = %v, want invalid params naming log_path", err)
	}
	if _, err := run(t, hostLog, `{"log_path":"`+hostLog+`"}`); err != nil {
		t.Errorf("same path should be allowed: %v", err)
	}
}
