package outboxreconcile_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/outboxreconcile"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
)

// noOpURLCfg is the zero-value URLConfig: no tracking params stripped, no
// default-port stripping, no trailing-slash policy. NormalizeURL still
// lowercases scheme/host and drops the fragment, but otherwise passes the
// plain example.com URLs used below through unchanged.
var noOpURLCfg = normalization.URLConfig{}

func TestMatchInHistory_PositiveMatch(t *testing.T) {
	sentAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	messages := []telegramimport.Message{
		{ID: 42, PostedAt: sentAt.Add(30 * time.Second), Text: "Leer más: https://example.com/a"},
		{ID: 41, PostedAt: sentAt.Add(-time.Hour), Text: "unrelated https://example.com/other"},
	}

	id, matched, covered := outboxreconcile.MatchInHistory(messages, "https://example.com/a", sentAt, noOpURLCfg, nil, 2*time.Minute)
	if !matched || !covered || id != "42" {
		t.Fatalf("MatchInHistory() = (%q, %v, %v), want (\"42\", true, true)", id, matched, covered)
	}
}

func TestMatchInHistory_ConfidentNegative(t *testing.T) {
	sentAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	messages := []telegramimport.Message{
		{ID: 41, PostedAt: sentAt.Add(-time.Minute), Text: "https://example.com/other"},
		{ID: 40, PostedAt: sentAt.Add(-time.Hour), Text: "https://example.com/older"}, // reaches back past sentAt-clockSkew
	}

	id, matched, covered := outboxreconcile.MatchInHistory(messages, "https://example.com/a", sentAt, noOpURLCfg, nil, 2*time.Minute)
	if matched || !covered || id != "" {
		t.Fatalf("MatchInHistory() = (%q, %v, %v), want (\"\", false, true)", id, matched, covered)
	}
}

func TestMatchInHistory_InconclusiveWhenCoverageDoesNotReachSentAt(t *testing.T) {
	sentAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	messages := []telegramimport.Message{
		{ID: 41, PostedAt: sentAt.Add(5 * time.Minute), Text: "https://example.com/other"}, // oldest fetched message is AFTER sentAt
	}

	_, matched, covered := outboxreconcile.MatchInHistory(messages, "https://example.com/a", sentAt, noOpURLCfg, nil, 2*time.Minute)
	if matched || covered {
		t.Fatalf("matched=%v covered=%v, want matched=false covered=false (fetch never reached back to sentAt)", matched, covered)
	}
}

func TestMatchInHistory_EmptyHistoryIsInconclusive(t *testing.T) {
	_, matched, covered := outboxreconcile.MatchInHistory(nil, "https://example.com/a", time.Now(), noOpURLCfg, nil, 2*time.Minute)
	if matched || covered {
		t.Fatalf("matched=%v covered=%v, want both false on empty history", matched, covered)
	}
}

// TestMatchInHistory_MatchExactlyAtClockSkewCutoffCounts pins the
// inclusive boundary: a message posted at exactly sentAt-clockSkew (not
// one tick later) must still count as within tolerance.
func TestMatchInHistory_MatchExactlyAtClockSkewCutoffCounts(t *testing.T) {
	sentAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	clockSkew := 2 * time.Minute
	messages := []telegramimport.Message{
		{ID: 7, PostedAt: sentAt.Add(-clockSkew), Text: "https://example.com/a"},
	}

	id, matched, covered := outboxreconcile.MatchInHistory(messages, "https://example.com/a", sentAt, noOpURLCfg, nil, clockSkew)
	if !matched || !covered || id != "7" {
		t.Fatalf("MatchInHistory() = (%q, %v, %v), want (\"7\", true, true) for a message exactly at the clock-skew cutoff", id, matched, covered)
	}
}

// TestMatchInHistory_CoverageExactlyAtCutoffCounts pins the same
// inclusive boundary for the coverage check: the oldest fetched message
// landing exactly on sentAt-clockSkew is enough to call the search
// covered, even with no match found.
func TestMatchInHistory_CoverageExactlyAtCutoffCounts(t *testing.T) {
	sentAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	clockSkew := 2 * time.Minute
	messages := []telegramimport.Message{
		{ID: 7, PostedAt: sentAt.Add(-clockSkew), Text: "https://example.com/other"},
	}

	id, matched, covered := outboxreconcile.MatchInHistory(messages, "https://example.com/a", sentAt, noOpURLCfg, nil, clockSkew)
	if matched || !covered || id != "" {
		t.Fatalf("MatchInHistory() = (%q, %v, %v), want (\"\", false, true) when oldest message lands exactly on the cutoff", id, matched, covered)
	}
}
