package backtestcandidate

import (
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestWriteCSV_UsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.csv")
	if err := writeCSV(path, nil); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, path, 0o600)
}
