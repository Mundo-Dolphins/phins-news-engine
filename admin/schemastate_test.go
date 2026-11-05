package admin

import (
	"errors"
	"testing"
	"time"
)

func TestSchemaStateRechecksOnlyWhileBlockedAndAfterTTL(t *testing.T) {
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }
	calls := 0
	verdict := errors.New("behind")
	s := newSchemaState(func() error { calls++; return verdict }, 30*time.Second, now)
	if calls != 1 || s.Blocked() == nil {
		t.Fatalf("startup: calls=%d", calls)
	}
	clock = clock.Add(10 * time.Second)
	if s.Blocked() == nil || calls != 1 {
		t.Fatalf("within TTL must not recheck: calls=%d", calls)
	}
	clock = clock.Add(25 * time.Second)
	if s.Blocked() == nil || calls != 2 {
		t.Fatalf("still blocked after TTL: calls=%d", calls)
	}
	verdict = nil
	clock = clock.Add(31 * time.Second)
	if s.Blocked() != nil || calls != 3 {
		t.Fatalf("should clear: calls=%d", calls)
	}
	clock = clock.Add(time.Hour)
	if s.Blocked() != nil || calls != 3 {
		t.Fatalf("open guard must not be rechecked: calls=%d", calls)
	}
}
