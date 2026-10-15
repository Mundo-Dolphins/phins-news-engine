package clusterlifecycle_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clusterlifecycle"
)

func TestEligible_BoundaryIsInclusive(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	closeAfter := 96 * time.Hour

	atBoundary := now.Add(-closeAfter)
	if !clusterlifecycle.Eligible(atBoundary, now, closeAfter) {
		t.Error("Eligible() at exact boundary = false, want true (inclusive)")
	}

	oneNanoBeforeBoundary := atBoundary.Add(time.Nanosecond)
	if clusterlifecycle.Eligible(oneNanoBeforeBoundary, now, closeAfter) {
		t.Error("Eligible() one nanosecond short of the boundary = true, want false")
	}
}

func TestEligible_WellPastCloseAfter(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	lastMemberAt := now.Add(-200 * time.Hour)
	if !clusterlifecycle.Eligible(lastMemberAt, now, 96*time.Hour) {
		t.Error("Eligible() 200h inactive with a 96h closeAfter = false, want true")
	}
}

func TestEligible_RecentActivityNotEligible(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	lastMemberAt := now.Add(-50 * time.Hour)
	if clusterlifecycle.Eligible(lastMemberAt, now, 96*time.Hour) {
		t.Error("Eligible() 50h inactive with a 96h closeAfter = true, want false")
	}
}
