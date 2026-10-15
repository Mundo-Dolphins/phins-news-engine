// Package repofs resolves test fixture paths against the module root.
//
// Tests must only read fixtures that live inside this repository. Building
// paths with a chain of ".." segments breaks when packages move and can
// silently resolve to a directory outside the repo. Path anchors every
// fixture at the module root and fails the test if the result escapes it.
package repofs

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ModulePath is the module this package expects to find in the root go.mod.
const ModulePath = "github.com/Mundo-Dolphins/phins-news-engine"

// Root returns the absolute path of the module root, found by walking up
// from the working directory to the go.mod that declares ModulePath.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isModuleRoot(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repofs: no go.mod for %s above the working directory", ModulePath)
		}
		dir = parent
	}
}

func isModuleRoot(goMod string) bool {
	f, err := os.Open(goMod)
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")) == ModulePath
		}
	}
	return false
}

// ErrEscapesRoot is returned when a fixture path resolves outside the root.
var ErrEscapesRoot = errors.New("repofs: path escapes the module root")

// Resolve joins rel onto root and checks that the result stays inside root.
func Resolve(root string, rel ...string) (string, error) {
	p := filepath.Join(append([]string{root}, rel...)...)
	r, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("%w: %s", ErrEscapesRoot, filepath.Join(rel...))
	}
	return p, nil
}

// Path returns the absolute path of a fixture given relative to the module
// root (for example Path(t, "configs", "example", "settings.yml")). It fails
// the test if the path would leave the repository.
func Path(t testing.TB, rel ...string) string {
	t.Helper()
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Resolve(root, rel...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ExampleConfig returns the path of a file under configs/example.
func ExampleConfig(t testing.TB, name string) string {
	t.Helper()
	return Path(t, "configs", "example", name)
}
