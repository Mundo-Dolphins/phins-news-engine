package publishing_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

func TestLagStats_NearestRankPercentiles(t *testing.T) {
	var lags []time.Duration
	for i := 1; i <= 100; i++ {
		lags = append(lags, time.Duration(101-i)*time.Minute) // unsorted on purpose
	}
	got := publishing.NewLagStats(lags)
	if got.Samples != 100 || got.P50Seconds != 50*60 || got.P95Seconds != 95*60 || got.MaxSeconds != 100*60 {
		t.Fatalf("NewLagStats = %+v, want samples=100 p50=50m p95=95m max=100m", got)
	}
}

func TestLagStats_EmptyIsZero(t *testing.T) {
	if got := publishing.NewLagStats(nil); got != (publishing.LagStats{}) {
		t.Fatalf("NewLagStats(nil) = %+v, want zero value", got)
	}
}

func TestEvaluateBacklog(t *testing.T) {
	th := publishing.DefaultBacklogThresholds()
	cases := []struct {
		name      string
		snap      publishing.BacklogSnapshot
		wantLevel publishing.BacklogLevel
		wantAlert []string
	}{
		{"healthy", publishing.BacklogSnapshot{BacklogCount: 3, OldestPendingAgeSeconds: 120}, publishing.BacklogOK, nil},
		{"empty", publishing.BacklogSnapshot{}, publishing.BacklogOK, nil},
		{"warning by count", publishing.BacklogSnapshot{BacklogCount: 30}, publishing.BacklogWarning, []string{"pending_count"}},
		{"critical by count", publishing.BacklogSnapshot{BacklogCount: 51}, publishing.BacklogCritical, []string{"pending_count"}},
		{"count at critical threshold is warning", publishing.BacklogSnapshot{BacklogCount: 50}, publishing.BacklogWarning, []string{"pending_count"}},
		{"critical by age", publishing.BacklogSnapshot{BacklogCount: 1, OldestPendingAgeSeconds: 2*3600 + 1}, publishing.BacklogCritical, []string{"oldest_pending_age"}},
		{"warning by age", publishing.BacklogSnapshot{BacklogCount: 1, OldestPendingAgeSeconds: 3601}, publishing.BacklogWarning, []string{"oldest_pending_age"}},
		{"critical by lag p95", publishing.BacklogSnapshot{Lag: publishing.LagStats{Samples: 5, P95Seconds: 6*3600 + 1}}, publishing.BacklogCritical, []string{"lag_p95"}},
		{"worst level wins, all alerts listed", publishing.BacklogSnapshot{BacklogCount: 30, OldestPendingAgeSeconds: 3 * 3600}, publishing.BacklogCritical, []string{"pending_count", "oldest_pending_age"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, alerts := publishing.EvaluateBacklog(tc.snap, th)
			if level != tc.wantLevel {
				t.Fatalf("level = %s, want %s (alerts %+v)", level, tc.wantLevel, alerts)
			}
			var metrics []string
			for _, a := range alerts {
				metrics = append(metrics, a.Metric)
			}
			if len(metrics) != len(tc.wantAlert) {
				t.Fatalf("alerts = %v, want %v", metrics, tc.wantAlert)
			}
			for i := range metrics {
				if metrics[i] != tc.wantAlert[i] {
					t.Fatalf("alerts = %v, want %v", metrics, tc.wantAlert)
				}
			}
		})
	}
}

func TestBacklogReport_JSONShapeIsStable(t *testing.T) {
	r := publishing.NewBacklogReport(publishing.BacklogSnapshot{
		Statuses:     []publishing.BacklogStatusCount{{Status: "pending", Lane: "normal", Count: 2}},
		Deferrals:    []publishing.ReasonCount{{Reason: "burst_limit", Count: 4}},
		Lag:          publishing.LagStats{Samples: 1, P50Seconds: 60, P95Seconds: 60, MaxSeconds: 60},
		BacklogCount: 2,
	}, publishing.DefaultBacklogThresholds())
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"level", "alerts", "thresholds", "generated_at", "window_seconds", "statuses", "backlog_count",
		"oldest_pending_age_seconds", "lag", "enqueued", "delivered", "expired", "superseded", "deferrals",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON missing key %q: %s", k, b)
		}
	}
	if m["level"] != "ok" {
		t.Errorf("level = %v, want ok", m["level"])
	}
	if a, ok := m["alerts"].([]any); !ok || len(a) != 0 {
		t.Errorf("alerts = %v, want empty array (never null)", m["alerts"])
	}
}
