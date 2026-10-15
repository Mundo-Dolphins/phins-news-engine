package docscheck_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/cli"
	"github.com/Mundo-Dolphins/phins-news-engine/docscheck"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// markdownFiles lists repo-relative Markdown files: README.md at the root
// and everything under docs/.
func markdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".md") && (rel == "README.md" || strings.HasPrefix(rel, "docs/")) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func report(t *testing.T, ps []docscheck.Problem) {
	t.Helper()
	for _, p := range ps {
		t.Error(p.String())
	}
}

// TestDocs validates the library's own Markdown (links, anchors, repo
// paths, Mermaid, YAML snippets, secrets and documented commands) against
// the public tree: the tools command tree is cli.Root() with no private
// commands registered.
func TestDocs(t *testing.T) {
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]*docscheck.Doc{}
	parse := func(rel string) (*docscheck.Doc, error) {
		if d, ok := cache[rel]; ok {
			return d, nil
		}
		d, err := docscheck.Parse(root, rel)
		if err != nil {
			return nil, err
		}
		cache[rel] = d
		return d, nil
	}

	targets, err := docscheck.MakeTargets(root)
	if err != nil {
		t.Fatal(err)
	}
	tree := docscheck.CLI{Tools: cli.Root(), MakeTargets: targets}

	strict := func(data []byte) error {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var cfg config.Config
		return dec.Decode(&cfg)
	}

	files := markdownFiles(t, root)
	if len(files) == 0 {
		t.Fatal("no markdown files found")
	}
	for _, rel := range files {
		d, err := parse(rel)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		t.Run(rel, func(t *testing.T) {
			report(t, docscheck.CheckLinks(root, d, parse))
			report(t, docscheck.CheckSecrets(d))
			report(t, docscheck.CheckMermaid(d))
			report(t, docscheck.CheckPathRefs(root, d))
			report(t, docscheck.CheckYAML(d, strict))
			report(t, docscheck.CheckCommands(d, tree))
		})
	}
}

// TestConfigReferenceCoversSettings fails when a section or second-level
// key of the example settings file is not mentioned in
// docs/CONFIG-REFERENCE.md, either by its full path or by a "section.*"
// group row.
func TestConfigReferenceCoversSettings(t *testing.T) {
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "configs", "example", "settings.yml"))
	if err != nil {
		t.Fatal(err)
	}
	refRaw, err := os.ReadFile(filepath.Join(root, "docs", "CONFIG-REFERENCE.md"))
	if err != nil {
		t.Fatal(err)
	}
	ref := strings.ReplaceAll(string(refRaw), `\`, "")

	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	documented := func(path, parent string) bool {
		if strings.Contains(ref, "`"+path+"`") || strings.Contains(ref, "`"+path+".") || strings.Contains(ref, path+",") {
			return true
		}
		return parent != "" && strings.Contains(ref, parent+".*")
	}
	for top, v := range cfg {
		if !documented(top, "") && !strings.Contains(ref, top+".") {
			t.Errorf("settings section %q is not documented in docs/CONFIG-REFERENCE.md", top)
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for k := range sub {
			if path := top + "." + k; !documented(path, top) {
				t.Errorf("settings key %q is not documented in docs/CONFIG-REFERENCE.md (add a row or a %s.* group row)", path, top)
			}
		}
	}
}
