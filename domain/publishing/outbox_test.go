package publishing_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

var backoff = publishing.BackoffPolicy{
	BaseDelay:   30 * time.Second,
	MaxDelay:    30 * time.Minute,
	MaxAttempts: 6,
}

func TestBackoffPolicy_NextAvailableAt(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 30 * time.Second},
		{attempt: 2, want: 60 * time.Second},
		{attempt: 3, want: 120 * time.Second},
		{attempt: 4, want: 240 * time.Second},
		{attempt: 5, want: 480 * time.Second},
		{attempt: 6, want: 960 * time.Second},
		{attempt: 7, want: 30 * time.Minute},  // capped
		{attempt: 20, want: 30 * time.Minute}, // still capped
	}
	for _, tt := range tests {
		got := backoff.NextAvailableAt(now, tt.attempt)
		want := now.Add(tt.want)
		if !got.Equal(want) {
			t.Errorf("NextAvailableAt(attempt=%d) = %v, want %v", tt.attempt, got, want)
		}
	}
}

func TestBackoffPolicy_Exhausted(t *testing.T) {
	tests := []struct {
		attempt int
		want    bool
	}{
		{attempt: 1, want: false},
		{attempt: 5, want: false},
		{attempt: 6, want: true},
		{attempt: 7, want: true},
	}
	for _, tt := range tests {
		if got := backoff.Exhausted(tt.attempt); got != tt.want {
			t.Errorf("Exhausted(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestValidRepairAction(t *testing.T) {
	tests := []struct {
		action publishing.RepairAction
		want   bool
	}{
		{publishing.RepairDeliver, true},
		{publishing.RepairRetry, true},
		{publishing.RepairDiscard, true},
		{publishing.RepairAction("bogus"), false},
		{publishing.RepairAction(""), false},
	}
	for _, tt := range tests {
		if got := publishing.ValidRepairAction(tt.action); got != tt.want {
			t.Errorf("ValidRepairAction(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}

func TestOutboxStatusValues(t *testing.T) {
	// Pins the exact string values migration 0023's CHECK constraint and
	// CASE mapping depend on — a drift here silently breaks the migration.
	tests := map[publishing.OutboxStatus]string{
		publishing.OutboxPending:         "pending",
		publishing.OutboxLeased:          "leased",
		publishing.OutboxSentUnconfirmed: "sent_unconfirmed",
		publishing.OutboxDelivered:       "delivered",
		publishing.OutboxRetryable:       "retryable",
		publishing.OutboxQuarantined:     "quarantined",
		publishing.OutboxDead:            "dead",
	}
	for status, want := range tests {
		if string(status) != want {
			t.Errorf("status %v = %q, want %q", status, string(status), want)
		}
	}
}
