package repofs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestRoot_FindsTheModuleGoMod(t *testing.T) {
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("Root() = %s has no go.mod: %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "testsupport", "repofs")); err != nil {
		t.Fatalf("Root() = %s does not contain testsupport/repofs: %v", root, err)
	}
}

func TestResolve_RejectsPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, rel := range [][]string{
		{".."},
		{"..", "configs", "settings.yml"},
		{"configs", "..", "..", "x"},
		{"a", "..", "..", "..", "b"},
	} {
		if _, err := repofs.Resolve(root, rel...); !errors.Is(err, repofs.ErrEscapesRoot) {
			t.Errorf("Resolve(%v) error = %v, want ErrEscapesRoot", rel, err)
		}
	}
	for _, rel := range [][]string{
		{"configs", "example", "settings.yml"},
		{"a", "..", "b"},
		{"..x"},
	} {
		if _, err := repofs.Resolve(root, rel...); err != nil {
			t.Errorf("Resolve(%v) error = %v, want nil", rel, err)
		}
	}
}

func TestPath_ExampleConfigsExist(t *testing.T) {
	for _, name := range []string{"settings.yml", "sources.yml", "example_dictionary.yml", "source-policies.yml"} {
		p := repofs.ExampleConfig(t, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("example config %s: %v", name, err)
		}
	}
}
