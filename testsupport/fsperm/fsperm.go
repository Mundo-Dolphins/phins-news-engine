// Package fsperm asserts file and directory permission bits in tests of
// code that writes host data (models, reports, roster files, databases).
package fsperm

import (
	"os"
	"testing"
)

// Assert fails t unless path exists with exactly the permission bits want.
func Assert(t testing.TB, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
