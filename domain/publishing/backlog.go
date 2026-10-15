package publishing

import (
	"math"
	"sort"
	"time"
)

// BacklogLevel is the overall severity of a backlog report. Only
// BacklogCritical is meant to fail a health check (non-zero exit).
type BacklogLevel string

const (
	BacklogOK       BacklogLevel = "ok"
	BacklogWarning  BacklogLevel = "warning"
	BacklogCritical BacklogLevel = "critical"
)

// BacklogStatusCount is one (status, lane) cell. Both values come from
// closed enums, so the number of cells is bounded.
type BacklogStatusCount struct {
	Status string `json:"status"`
	Lane   string `json:"lane"`
	Count  int    `json:"count"`
}

// ReasonCount groups events by a stable reason code. Producers cap the
// number of distinct reasons, so this never carries article ids or titles.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// LagStats summarizes sent_at - articles.published_at for items delivered
// in the report window. Lag measures how stale content was when it reached
// the channel, so it is anchored on published_at; queue age (see
// BacklogSnapshot.OldestPendingAgeSeconds) is anchored on created_at,
// which is when the bot enqueued the item.
type LagStats struct {
	Samples    int   `json:"samples"`
	P50Seconds int64 `json:"p50_seconds"`
	P95Seconds int64 `json:"p95_seconds"`
	MaxSeconds int64 `json:"max_seconds"`
}

// NewLagStats computes nearest-rank percentiles; no samples gives the zero value.
func NewLagStats(lags []time.Duration) LagStats {
	if len(lags) == 0 {
		return LagStats{}
	}
	sorted := append([]time.Duration(nil), lags...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(p float64) int64 {
		i := int(math.Ceil(p*float64(len(sorted)))) - 1
		return int64(sorted[i] / time.Second)
	}
	return LagStats{Samples: len(sorted), P50Seconds: rank(0.50), P95Seconds: rank(0.95), MaxSeconds: int64(sorted[len(sorted)-1] / time.Second)}
}

// BacklogSnapshot is the raw measurement of the publication queue.
// BacklogCount counts claimable rows (pending + retryable); the oldest age
// is measured from publication_outbox.created_at of those rows.
type BacklogSnapshot struct {
	GeneratedAt             time.Time            `json:"generated_at"`
	WindowSeconds           int64                `json:"window_seconds"`
	Statuses                []BacklogStatusCount `json:"statuses"`
	BacklogCount            int                  `json:"backlog_count"`
	OldestPendingAgeSeconds int64                `json:"oldest_pending_age_seconds"`
	Lag                     LagStats             `json:"lag"`
	Enqueued                int                  `json:"enqueued"`
	Delivered               int                  `json:"delivered"`
	Expired                 int                  `json:"expired"`
	Superseded              int                  `json:"superseded"`
	Deferrals               []ReasonCount        `json:"deferrals"`
}

// BacklogThresholds are strict "greater than" limits per metric.
type BacklogThresholds struct {
	PendingWarning  int           `json:"pending_warning"`
	PendingCritical int           `json:"pending_critical"`
	AgeWarning      time.Duration `json:"-"`
	AgeCritical     time.Duration `json:"-"`
	LagP95Warning   time.Duration `json:"-"`
	LagP95Critical  time.Duration `json:"-"`
}

// backlogThresholdsJSON is the JSON view of BacklogThresholds: durations in seconds.
type backlogThresholdsJSON struct {
	PendingWarning        int   `json:"pending_warning"`
	PendingCritical       int   `json:"pending_critical"`
	AgeWarningSeconds     int64 `json:"age_warning_seconds"`
	AgeCriticalSeconds    int64 `json:"age_critical_seconds"`
	LagP95WarningSeconds  int64 `json:"lag_p95_warning_seconds"`
	LagP95CriticalSeconds int64 `json:"lag_p95_critical_seconds"`
}

// DefaultBacklogThresholds: critical at >50 pending or oldest >2h.
func DefaultBacklogThresholds() BacklogThresholds {
	return BacklogThresholds{
		PendingWarning: 25, PendingCritical: 50,
		AgeWarning: time.Hour, AgeCritical: 2 * time.Hour,
		LagP95Warning: 3 * time.Hour, LagP95Critical: 6 * time.Hour,
	}
}

// BacklogAlert is one threshold breach.
type BacklogAlert struct {
	Metric    string       `json:"metric"`
	Level     BacklogLevel `json:"level"`
	Value     int64        `json:"value"`
	Threshold int64        `json:"threshold"`
}

// EvaluateBacklog returns the worst level and every breached metric, in a
// fixed metric order.
func EvaluateBacklog(s BacklogSnapshot, t BacklogThresholds) (BacklogLevel, []BacklogAlert) {
	alerts := []BacklogAlert{}
	check := func(metric string, value, warn, crit int64) {
		switch {
		case crit > 0 && value > crit:
			alerts = append(alerts, BacklogAlert{metric, BacklogCritical, value, crit})
		case warn > 0 && value > warn:
			alerts = append(alerts, BacklogAlert{metric, BacklogWarning, value, warn})
		}
	}
	check("pending_count", int64(s.BacklogCount), int64(t.PendingWarning), int64(t.PendingCritical))
	check("oldest_pending_age", s.OldestPendingAgeSeconds, int64(t.AgeWarning/time.Second), int64(t.AgeCritical/time.Second))
	check("lag_p95", s.Lag.P95Seconds, int64(t.LagP95Warning/time.Second), int64(t.LagP95Critical/time.Second))
	level := BacklogOK
	for _, a := range alerts {
		if a.Level == BacklogCritical {
			level = BacklogCritical
		} else if level == BacklogOK {
			level = BacklogWarning
		}
	}
	return level, alerts
}

// BacklogReport is the stable contract shared by the human and JSON outputs.
type BacklogReport struct {
	BacklogSnapshot
	Level      BacklogLevel          `json:"level"`
	Alerts     []BacklogAlert        `json:"alerts"`
	Thresholds backlogThresholdsJSON `json:"thresholds"`
}

// NewBacklogReport evaluates snapshot against thresholds.
func NewBacklogReport(s BacklogSnapshot, t BacklogThresholds) BacklogReport {
	if s.Statuses == nil {
		s.Statuses = []BacklogStatusCount{}
	}
	if s.Deferrals == nil {
		s.Deferrals = []ReasonCount{}
	}
	level, alerts := EvaluateBacklog(s, t)
	return BacklogReport{BacklogSnapshot: s, Level: level, Alerts: alerts, Thresholds: backlogThresholdsJSON{
		t.PendingWarning, t.PendingCritical,
		int64(t.AgeWarning / time.Second), int64(t.AgeCritical / time.Second),
		int64(t.LagP95Warning / time.Second), int64(t.LagP95Critical / time.Second),
	}}
}
