package repofs_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// finding is one fixture-path problem reported by analyzeSource.
type finding struct {
	Line int
	Msg  string
}

func (f finding) String() string { return fmt.Sprintf("line %d: %s", f.Line, f.Msg) }

// absPathLit matches a string literal that is an absolute path into a
// user, system or temporary tree. A test that names such a path reads (or
// writes) outside the repository.
var absPathLit = regexp.MustCompile(`^/(Users|home|root|private|tmp|var|etc|opt|mnt|Volumes|srv|data)(/|$)`)

// absPathAllowlist lists "file:literal" pairs that look like absolute
// paths but are never opened. Each entry needs a reason.
var absPathAllowlist = map[string]string{
	// GeneratedPath only echoes the configured value back; nothing is read.
	"cli/cmd/events/cycle_test.go:/tmp/x.yml": "config value echoed back, never opened",
	// A candidate's Dir label in an in-memory result; ranking never opens it.
	"app/trainrun/selection_internal_test.go:/tmp/": "label in an in-memory struct, never opened",
	// The env override is compared as a string; Load never opens the DB.
	"infra/config/config_test.go:/tmp/override.db": "env override compared as a string, never opened",
}

// analyzeSource is the pure core of the guard. It parses src (the text of
// the Go file at repo-relative path rel, slash-separated) and reports:
//
//  1. a filepath.Join / path.Join whose ".." arguments climb above the
//     module root. Every argument is tracked: a string literal pushes or
//     pops its segments, and any other expression (a variable such as the
//     working directory) restarts from the file's own directory, which is
//     where `go test` runs it;
//  2. a string literal starting with "../" or "./../" that resolves
//     outside the module root from the file's directory;
//  3. in _test.go files, an absolute path literal into a user or system
//     tree ("/Users/...", "/home/...", ...);
//  4. in _test.go files, a read of the home directory (os.UserHomeDir,
//     os.Getenv("HOME"), os.LookupEnv("HOME")), which would root a fixture
//     path outside the repository. t.Setenv("HOME", ...) is not a read and
//     is allowed.
func analyzeSource(rel string, src []byte) ([]finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	isTest := strings.HasSuffix(rel, "_test.go")
	dir := path.Dir(rel)
	var out []finding
	report := func(pos token.Pos, format string, args ...any) {
		out = append(out, finding{Line: fset.Position(pos).Line, Msg: fmt.Sprintf(format, args...)})
	}
	// escapes reports whether applying segs from dir leaves the root.
	escapes := func(segs []string) bool {
		depth := 0
		if dir != "." {
			depth = len(strings.Split(dir, "/"))
		}
		for _, s := range segs {
			for _, part := range strings.Split(filepath.ToSlash(s), "/") {
				switch part {
				case "", ".":
				case "..":
					depth--
					if depth < 0 {
						return true
					}
				default:
					depth++
				}
			}
		}
		return false
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if isJoinCall(n) {
				var segs []string
				for _, a := range n.Args {
					s, ok := stringLit(a)
					if !ok {
						segs = nil // unknown base: restart at the file's directory
						continue
					}
					if strings.HasPrefix(s, "/") {
						segs = nil
						continue
					}
					segs = append(segs, s)
					if escapes(segs) {
						report(n.Pos(), "Join(...) climbs outside the module root")
						break
					}
				}
			}
			if isTest && readsHome(n) {
				report(n.Pos(), "reads the home directory to build a path")
			}
		case *ast.BasicLit:
			s, ok := stringLit(n)
			if !ok || strings.ContainsAny(s, " \n%") {
				return true
			}
			lit := strings.TrimPrefix(s, "./")
			if strings.HasPrefix(lit, "../") && escapes([]string{lit}) {
				report(n.Pos(), "path %q resolves outside the module root", s)
			}
			if isTest && absPathLit.MatchString(s) {
				if _, ok := absPathAllowlist[rel+":"+s]; !ok {
					report(n.Pos(), "absolute path literal %q points outside the repository", s)
				}
			}
		}
		return true
	})
	return out, nil
}

// readsHome reports os.UserHomeDir(), os.Getenv("HOME") and
// os.LookupEnv("HOME").
func readsHome(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return false
	}
	switch sel.Sel.Name {
	case "UserHomeDir":
		return true
	case "Getenv", "LookupEnv":
		if len(c.Args) == 1 {
			s, ok := stringLit(c.Args[0])
			return ok && s == "HOME"
		}
	}
	return false
}

func isJoinCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && (pkg.Name == "filepath" || pkg.Name == "path")
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// TestNoTestFixturePathEscapesTheModuleRoot runs analyzeSource over every
// Go file in the module, so no test can read a sibling checkout or a home
// directory instead of an in-repo fixture.
func TestNoTestFixturePathEscapesTheModuleRoot(t *testing.T) {
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); p != root && (strings.HasPrefix(name, ".") || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fs, err := analyzeSource(filepath.ToSlash(rel), src)
		if err != nil {
			return err
		}
		for _, f := range fs {
			t.Errorf("%s: %s", rel, f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeSource_FlagsEachEscapePattern(t *testing.T) {
	cases := map[string]string{
		"join with a non-literal base":  `wd, _ := os.Getwd(); _ = filepath.Join(wd, "..", "..", "..", "configs", "x.yml")`,
		"leading .. literals":           `_ = filepath.Join("..", "..", "..", "configs", "x.yml")`,
		"multi-segment literal in join": `_ = path.Join("pkg", "../../../../x")`,
		"relative literal":              `_ = "../../../configs/x.yml"`,
		"dot-slash relative literal":    `_ = "./../../../configs/x.yml"`,
		"absolute home literal":         `_ = "/Users/someone/private/configs/x.yml"`,
		"absolute linux home literal":   `_ = "/home/someone/data.db"`,
		"os.UserHomeDir":                `h, _ := os.UserHomeDir(); _ = h`,
		"os.Getenv HOME":                `_ = filepath.Join(os.Getenv("HOME"), "x")`,
		"os.LookupEnv HOME":             `_, _ = os.LookupEnv("HOME")`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := analyzeSource("app/pkg/x_test.go", snippet(body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 {
				t.Fatalf("not flagged: %s", body)
			}
		})
	}
}

func TestAnalyzeSource_AllowsBenignPaths(t *testing.T) {
	cases := map[string]string{
		"join inside the root":    `_ = filepath.Join("..", "..", "testdata", "x.yml")`,
		"temp dir climb":          `dir := "d"; _ = filepath.Join(dir, "..", "sibling")`,
		"relative literal inside": `_ = "../../testdata/x.yml"`,
		"dot-slash inside":        `_ = "./testdata/x.yml"`,
		"absolute non-user path":  `_ = "/v1/models"`,
		"setenv HOME":             `var t interface{ Setenv(string, string) }; t.Setenv("HOME", "x")`,
		"getenv other variable":   `_ = os.Getenv("TZ")`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := analyzeSource("app/pkg/x_test.go", snippet(body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("flagged %v for: %s", got, body)
			}
		})
	}
	// Absolute and home-directory checks apply to test files only.
	got, err := analyzeSource("app/pkg/x.go", snippet(`_ = "/tmp/run.sock"; h, _ := os.UserHomeDir(); _ = h`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("non-test file flagged: %v", got)
	}
}

func snippet(body string) []byte {
	return []byte("package x\n\nimport (\n\t\"os\"\n\t\"path\"\n\t\"path/filepath\"\n)\n\nvar _ = os.Getwd\nvar _ = path.Join\nvar _ = filepath.Join\n\nfunc f() {\n\t" + body + "\n}\n")
}
