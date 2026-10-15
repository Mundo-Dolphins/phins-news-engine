package dictionarysession_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestSave_UsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := dictionarysession.Save(dir, dictionarysession.New("sess-1", nil, "1", "h", nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	fsperm.Assert(t, filepath.Join(dir, entries[0].Name()), 0o600)
}
