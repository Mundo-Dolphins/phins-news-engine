package fsperm_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestAssertMatchingMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, p, 0o600)
}

// recorder captures failures instead of failing the real test.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (r *recorder) Fatalf(string, ...any) { r.failed = true }

func TestAssertReportsMismatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{TB: t}
	fsperm.Assert(r, p, 0o644)
	if !r.failed {
		t.Fatal("mismatch not reported")
	}
}
