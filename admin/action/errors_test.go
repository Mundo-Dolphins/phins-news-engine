package action

import (
	"errors"
	"testing"
)

func TestErrorUnwrap(t *testing.T) {
	base := errors.New("boom")
	e := NotFound(base)
	if e.Code != CodeNotFound || !errors.Is(e, base) || e.Error() == "" {
		t.Fatalf("NotFound = %+v", e)
	}
	c := Conflict(base)
	if c.Code != CodeConflict || !errors.Is(c, base) {
		t.Fatalf("Conflict = %+v", c)
	}
	var target *Error
	if !errors.As(error(c), &target) || target.Code != CodeConflict {
		t.Fatal("errors.As failed")
	}
}
