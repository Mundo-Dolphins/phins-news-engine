package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func writeDoc(t *testing.T, root, rel, body string) *Doc {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Parse(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func joined(ps []Problem) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.String())
	}
	return strings.Join(s, "\n")
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Online pipeline":                      "online-pipeline",
		"`train promote` / `train rollback`":   "train-promote--train-rollback",
		"Migrating to `editorial.mode` (#168)": "migrating-to-editorialmode-168",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckLinks(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "docs/b.md", "# B\n\n## Real heading\n")
	d := writeDoc(t, root, "docs/a.md", strings.Join([]string{
		"# A",
		"[ok](b.md#real-heading) [bad file](nope.md) [bad anchor](b.md#missing)",
		"[self](#a) [ext](https://example.com/x) `[code](ignored.md)`",
		"[escape](../../x.md)",
	}, "\n"))
	parse := func(rel string) (*Doc, error) { return Parse(root, rel) }
	got := joined(CheckLinks(root, d, parse))
	for _, want := range []string{"nope.md", "#missing", "escapes the repository"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem about %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"real-heading", "#a\"", "example.com", "ignored.md"} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected problem mentioning %q in:\n%s", bad, got)
		}
	}
}

func TestCheckPathRefs(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "configs/real.yml", "a: 1\n")
	if err := os.MkdirAll(filepath.Join(root, "app", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := writeDoc(t, root, "docs/a.md", "`configs/real.yml` `configs/gone.yml` `app/pkg.Func` `app/<x>/y.go` `configs/real.yml:12`\n")
	got := joined(CheckPathRefs(root, d))
	if !strings.Contains(got, "configs/gone.yml") || strings.Count(got, "\n") != 0 {
		t.Errorf("want exactly one problem about configs/gone.yml, got:\n%s", got)
	}
}

func TestCheckMermaid(t *testing.T) {
	root := t.TempDir()
	good := writeDoc(t, root, "good.md", "```mermaid\nflowchart LR\n  A[\"x (y)\"] --> B{ok}\n```\n")
	if ps := CheckMermaid(good); len(ps) != 0 {
		t.Errorf("good diagram flagged: %v", ps)
	}
	bad := writeDoc(t, root, "bad.md", "```mermaid\nflowhcart LR\n  A --> B\n```\n\n```mermaid\nflowchart LR\n  A[open --> B\n```\n")
	if ps := CheckMermaid(bad); len(ps) != 2 {
		t.Errorf("want 2 problems, got %v", ps)
	}
}

func TestCheckYAMLAndSkip(t *testing.T) {
	root := t.TempDir()
	d := writeDoc(t, root, "a.md", "```yaml\na: [1\n```\n\n<!-- docs-check:skip -->\n```yaml\nb: [\n```\n\n```yaml settings\nx: 1\n```\n")
	strict := func([]byte) error { return os.ErrInvalid }
	got := joined(CheckYAML(d, strict))
	if !strings.Contains(got, "invalid YAML") || !strings.Contains(got, "strict config") {
		t.Errorf("expected YAML and strict problems, got:\n%s", got)
	}
	if strings.Count(got, "invalid YAML") != 1 {
		t.Errorf("skip marker not honoured:\n%s", got)
	}
}

// realLookingChannelID is a made-up id in the shape of a real channel id,
// assembled at run time so the literal never appears in the tree.
var realLookingChannelID = "-100" + "9999999999"

func TestCheckSecrets(t *testing.T) {
	root := t.TempDir()
	d := writeDoc(t, root, "a.md", strings.Join([]string{
		"placeholder -1000000000000 is fine",
		"real " + realLookingChannelID + " is not",
		"token 123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA is not",
		"api_hash: " + strings.Repeat("0123456789abcdef", 2) + " is not", // built at run time so secret scanners do not flag this fake
	}, "\n"))
	got := joined(CheckSecrets(d))
	if strings.Count(got, "\n") != 2 || strings.Contains(got, "a.md:1:") {
		t.Errorf("want problems on lines 2-4 only, got:\n%s", got)
	}
}

func fakeCLI() CLI {
	root := &cobra.Command{Use: "example-tools"}
	root.PersistentFlags().String("config", "", "")
	train := &cobra.Command{Use: "train"}
	build := &cobra.Command{Use: "build", Run: func(*cobra.Command, []string) {}}
	build.Flags().String("out", "", "")
	build.Flags().Bool("dry-run", false, "")
	train.AddCommand(build)
	root.AddCommand(train)
	return CLI{
		Tools:       root,
		MakeTargets: map[string]bool{"build": true},
	}
}

func TestCheckCommands(t *testing.T) {
	root := t.TempDir()
	d := writeDoc(t, root, "a.md", strings.Join([]string{
		"```bash",
		"make build",
		"make nonsense",
		"./build/example-tools train build --out x --dry-run --config c",
		"./build/example-tools train export",
		"FOO=1 example-tools train build --bogus",
		"./build/example-tools train build \\",
		"  --also-bogus",
		"./build/example-tools train build <id> --not-checked-after-placeholder",
		"```",
		"",
		"<!-- docs-check:skip -->",
		"```bash",
		"make skipped",
		"```",
		"",
		"inline `make gone` and `example-tools train zip`.",
	}, "\n"))
	got := joined(CheckCommands(d, fakeCLI()))
	for _, want := range []string{`"nonsense"`, `no subcommand "export"`, `"--bogus"`, `"--also-bogus"`, `"gone"`, `"zip"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem %s in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n") + 1; n != 6 {
		t.Errorf("want exactly 6 problems, got %d:\n%s", n, got)
	}
}

func TestParseUnterminatedFence(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.md")
	if err := os.WriteFile(p, []byte("```bash\nmake x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(root, "a.md"); err == nil {
		t.Error("expected an unterminated-fence error")
	}
}
