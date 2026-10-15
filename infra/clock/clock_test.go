package clock_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
)

func TestReal_Now_ReturnsCurrentTime(t *testing.T) {
	before := time.Now()
	got := clock.Real{}.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want between %v and %v", got, before, after)
	}
}
