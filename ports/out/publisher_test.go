package out_test

import (
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestPermanentError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("400 bad request")
	err := &out.PermanentError{Err: inner}

	if err.Error() != inner.Error() {
		t.Errorf("Error() = %q, want %q", err.Error(), inner.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is(err, inner) = false, want true (Unwrap must expose Err)")
	}

	var target *out.PermanentError
	if !errors.As(fmtWrap(err), &target) {
		t.Error("errors.As should find *PermanentError even wrapped further")
	}
}

func fmtWrap(err error) error {
	return errWrap{err}
}

type errWrap struct{ err error }

func (e errWrap) Error() string { return "wrapped: " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }

func TestAmbiguousError_UnwrapAndAs(t *testing.T) {
	inner := errors.New("read tcp: connection reset by peer")
	err := &out.AmbiguousError{Err: inner}

	if !errors.Is(err, inner) {
		t.Error("errors.Is(err, inner) = false, want true (Unwrap must expose inner)")
	}

	var target *out.AmbiguousError
	if !errors.As(err, &target) {
		t.Fatal("errors.As into *out.AmbiguousError failed")
	}
	if target.Err != inner {
		t.Errorf("target.Err = %v, want %v", target.Err, inner)
	}
	if err.Error() == "" {
		t.Error("Error() must not be empty")
	}
}
