package dictionaryhistory_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestWriteVersion_UsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	a := dictionaryhistory.Artifacts{Version: "1.0.1", Dictionary: dictionary.Dictionary{Version: "1.0.1"}}
	if err := dictionaryhistory.WriteVersion(dir, a); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, dir, 0o700)
	version := filepath.Join(dir, "v1.0.1")
	fsperm.Assert(t, version, 0o700)
	entries, err := os.ReadDir(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fsperm.Assert(t, filepath.Join(version, e.Name()), 0o600)
	}
}
