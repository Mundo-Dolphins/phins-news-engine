package dictionarysnapshot_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestWrite_UsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshots")
	snap := dictionarysnapshot.New(dictionaryupdate.SourceKindRoster, "https://x.example.com", "x.example.com", "auto", 200, nil, nil, time.Now())
	path, err := dictionarysnapshot.Write(dir, snap, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, dir, 0o700)
	fsperm.Assert(t, path, 0o600)
}
