package editorial

import (
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestWriteEvalMisses_UsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "misses.csv")
	if err := writeEvalMisses(path, nil, 0.9); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, path, 0o600)
}
