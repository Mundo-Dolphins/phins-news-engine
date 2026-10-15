package editorialmodel_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestSave_UsesPrivatePermissions(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "models", "editorial")
	dir := filepath.Join(parent, "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes"}}
	if err := editorialmodel.Save(dir, sampleModel(), sampleManifest(), `{}`, schema); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, parent, 0o700)
	fsperm.Assert(t, dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fsperm.Assert(t, filepath.Join(dir, e.Name()), 0o600)
	}
}

func TestMoveAside_CreatesPrivateParent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "new-parent", "previous")
	if err := editorialmodel.MoveAside(src, dest); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, filepath.Dir(dest), 0o700)
}
