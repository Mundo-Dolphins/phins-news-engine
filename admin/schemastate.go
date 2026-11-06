package admin

import (
	"sync"
	"time"
)

// schemaRecheck is how often a blocked guard re-runs its check.
var schemaRecheck = 30 * time.Second

// schemaState holds the production schema guard's verdict. While writes are
// blocked it re-runs check at most once per ttl when asked, so an operator
// applying the missing migration externally unblocks the panel without a
// restart. Once check passes the block is cleared for good: an open guard is
// never re-checked. It never migrates.
type schemaState struct {
	mu    sync.Mutex
	check func() error
	ttl   time.Duration
	now   func() time.Time
	err   error
	last  time.Time
}

// newSchemaState runs check once and keeps its verdict.
func newSchemaState(check func() error, ttl time.Duration, now func() time.Time) *schemaState {
	s := &schemaState{check: check, ttl: ttl, now: now}
	s.err = check()
	s.last = now()
	return s
}

// Blocked returns the error blocking write and destructive actions, or nil.
func (s *schemaState) Blocked() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		return nil
	}
	if t := s.now(); t.Sub(s.last) >= s.ttl {
		s.err = s.check()
		s.last = t
	}
	return s.err
}
