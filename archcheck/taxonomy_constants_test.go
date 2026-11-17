// archcheck/taxonomy_constants_test.go

// Package archcheck_test holds architecture checks over the module's
// source tree.
package archcheck_test

import (
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

const (
	eventsignalsImport = "github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	eventsignalsDir    = "domain/eventsignals"
	eventsignalsTest   = "domain/eventsignals/eventsignalstest"
	constantsFile      = "domain/eventsignals/eventsignals.go"
)

// taxonomyConstantName matches the names of the taxonomy constants.
var taxonomyConstantName = regexp.MustCompile(`^(EventType|Action|ContentSignal)[A-Z][A-Za-z]*$`)

// taxonomyConstants returns the EventType, Action and ContentSignal
// constants declared in src (the text of eventsignals.go), without
// EventTypeGeneric: the fallback type every taxonomy has.
func taxonomyConstants(t *testing.T, src []byte) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), constantsFile, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if taxonomyConstantName.MatchString(n.Name) && n.Name != "EventTypeGeneric" {
					names[n.Name] = true
				}
			}
		}
	}
	return names
}

// constantReferences returns "line N: Name" for each reference to one of
// names in src, the Go file at repo-relative path rel. Inside the
// eventsignals package an unqualified identifier counts, outside it a
// selector on the eventsignals import (under any local name; a dot import
// makes unqualified identifiers count too). The const declarations of
// eventsignals.go are skipped; comments never count.
func constantReferences(rel string, src []byte, names map[string]bool) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	unqualified := path.Dir(rel) == eventsignalsDir
	local := ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != eventsignalsImport {
			continue
		}
		switch {
		case imp.Name == nil:
			local = "eventsignals"
		case imp.Name.Name == ".":
			unqualified = true
		default:
			local = imp.Name.Name
		}
	}
	var out []string
	report := func(n *ast.Ident) {
		out = append(out, "line "+strconv.Itoa(fset.Position(n.Pos()).Line)+": "+n.Name)
	}
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			return rel != constantsFile || n.Tok != token.CONST
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && local != "" && x.Name == local && names[n.Sel.Name] {
				report(n.Sel)
			}
			ast.Inspect(n.X, visit)
			return false // n.Sel names a field, method or qualified identifier
		case *ast.Ident:
			if unqualified && names[n.Name] {
				report(n)
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return out, nil
}

// TestProductionCodeDoesNotBranchOnTaxonomyConstants keeps the event
// vocabulary in the taxonomy configuration: production code may not name an
// EventType, Action or ContentSignal constant other than EventTypeGeneric.
// Tests and the eventsignalstest fixtures may.
func TestProductionCodeDoesNotBranchOnTaxonomyConstants(t *testing.T) {
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(constantsFile))) // #nosec G304 -- fixed repo path
	if err != nil {
		t.Fatal(err)
	}
	names := taxonomyConstants(t, src)
	if len(names) == 0 {
		t.Fatalf("no taxonomy constants found in %s", constantsFile)
	}
	var problems []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" || rel == eventsignalsTest) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		code, err := os.ReadFile(p) // #nosec G304 -- walked repo file
		if err != nil {
			return err
		}
		refs, err := constantReferences(rel, code, names)
		if err != nil {
			return err
		}
		for _, r := range refs {
			problems = append(problems, rel+": "+r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s: name it through the loaded taxonomy instead", p)
	}
}

func TestConstantReferences(t *testing.T) {
	names := map[string]bool{"ActionSign": true, "EventTypeGame": true}
	cases := map[string]struct {
		rel, src string
		want     []string
	}{
		"qualified outside": {
			"app/x/x.go",
			"package x\nimport \"" + eventsignalsImport + "\"\nvar a = eventsignals.ActionSign\n",
			[]string{"line 3: ActionSign"},
		},
		"aliased import": {
			"app/x/x.go",
			"package x\nimport es \"" + eventsignalsImport + "\"\nvar a = es.EventTypeGame\n",
			[]string{"line 3: EventTypeGame"},
		},
		"generic and comments allowed": {
			"app/x/x.go",
			"package x\nimport \"" + eventsignalsImport + "\"\n// eventsignals.ActionSign\nvar a = eventsignals.EventTypeGeneric\n",
			nil,
		},
		"same name in another package": {
			"app/x/x.go",
			"package x\nimport \"other\"\nvar a = other.ActionSign\nvar ActionSign = 1\n",
			nil,
		},
		"unqualified inside the package": {
			"domain/eventsignals/x.go",
			"package eventsignals\nfunc f() Action { return ActionSign }\n",
			[]string{"line 2: ActionSign"},
		},
		"declarations allowed": {
			constantsFile,
			"package eventsignals\nconst ActionSign Action = \"sign\"\nvar b = ActionSign\n",
			[]string{"line 3: ActionSign"},
		},
	}
	for name, c := range cases {
		got, err := constantReferences(c.rel, []byte(c.src), names)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
