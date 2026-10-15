// Package docscheck statically validates the repository's Markdown
// documentation against the code it describes: internal links and anchors,
// referenced repository paths, Mermaid and YAML snippets, and accidental
// secrets. It never touches the network. The checks that need the live
// command tree (documented CLI commands, flags and make targets) live in
// the package's tests, which import the Cobra root.
//
// A fenced block can opt out of the command/YAML checks by placing
// "<!-- docs-check:skip -->" on the line directly above it.
package docscheck

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Fence is a fenced code block extracted from a Markdown file.
type Fence struct {
	Lang string // first word of the info string, lowercased
	Info string // full info string
	Line int    // 1-based line of the opening fence
	Body []string
	Skip bool // preceded by the skip marker
}

// Doc is a parsed Markdown file.
type Doc struct {
	Path     string // repo-relative, slash-separated
	Lines    []string
	Prose    []ProseLine // lines outside fenced blocks
	Fences   []Fence
	Headings map[string]bool // GitHub-style anchor slugs
}

// ProseLine is a non-fenced line with its 1-based number.
type ProseLine struct {
	Line int
	Text string
}

// Problem is one validation failure.
type Problem struct {
	File string
	Line int
	Msg  string
}

func (p Problem) String() string { return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Msg) }

const skipMarker = "<!-- docs-check:skip -->"

var (
	fenceOpen  = regexp.MustCompile("^\\s*(```+|~~~+)\\s*(.*)$")
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	linkRe     = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	inlineCode = regexp.MustCompile("`([^`\n]+)`")
	slugStrip  = regexp.MustCompile(`[^\p{L}\p{N}\- _]`)
	htmlTag    = regexp.MustCompile(`<[^>]+>`)
	pathRef    = regexp.MustCompile(`^(?:\./)?(configs|domain|ports|app|adapters|infra|engine|cli|docscheck|testsupport|tools|docs|migrations|testdata|\.github)/[A-Za-z0-9_./-]*[A-Za-z0-9_]$`)
)

// Slug returns the GitHub anchor for a heading's text.
func Slug(heading string) string {
	h := inlineCode.ReplaceAllString(heading, "$1")
	h = htmlTag.ReplaceAllString(h, "")
	h = strings.ToLower(strings.TrimSpace(h))
	h = slugStrip.ReplaceAllString(h, "")
	return strings.ReplaceAll(h, " ", "-")
}

// Parse reads one Markdown file. root is the repository root and rel the
// slash-separated repo-relative path.
func Parse(root, rel string) (*Doc, error) {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304 -- test-only tool reading repo docs
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d := &Doc{Path: rel, Headings: map[string]bool{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var (
		cur      *Fence
		marker   string
		skipNext bool
		seen     = map[string]int{}
	)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		d.Lines = append(d.Lines, line)
		trimmed := strings.TrimSpace(line)

		if cur != nil {
			if strings.HasPrefix(trimmed, marker) && strings.Trim(trimmed, marker[:1]) == "" {
				d.Fences = append(d.Fences, *cur)
				cur = nil
				continue
			}
			cur.Body = append(cur.Body, line)
			continue
		}
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			info := strings.TrimSpace(m[2])
			lang := ""
			if fields := strings.Fields(info); len(fields) > 0 {
				lang = strings.ToLower(fields[0])
			}
			cur = &Fence{Lang: lang, Info: info, Line: n, Skip: skipNext}
			marker = m[1]
			skipNext = false
			continue
		}
		skipNext = trimmed == skipMarker
		d.Prose = append(d.Prose, ProseLine{Line: n, Text: line})
		if m := headingRe.FindStringSubmatch(line); m != nil {
			slug := Slug(m[2])
			if c := seen[slug]; c > 0 {
				d.Headings[fmt.Sprintf("%s-%d", slug, c)] = true
			} else {
				d.Headings[slug] = true
			}
			seen[slug]++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		return d, fmt.Errorf("%s:%d: unterminated code fence", rel, cur.Line)
	}
	return d, nil
}

// CheckLinks verifies every relative Markdown link and #anchor in d.
// parse resolves other documents for anchor lookups.
func CheckLinks(root string, d *Doc, parse func(rel string) (*Doc, error)) []Problem {
	var out []Problem
	dir := filepath.Dir(filepath.FromSlash(d.Path))
	for _, pl := range d.Prose {
		line := inlineCode.ReplaceAllString(pl.Text, "")
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path, anchor, _ := strings.Cut(target, "#")
			targetRel := d.Path
			if path != "" {
				targetRel = filepath.ToSlash(filepath.Join(dir, path))
				if strings.HasPrefix(targetRel, "../") || targetRel == ".." {
					out = append(out, Problem{d.Path, pl.Line, fmt.Sprintf("link %q escapes the repository", target)})
					continue
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(targetRel))); err != nil {
					out = append(out, Problem{d.Path, pl.Line, fmt.Sprintf("broken link %q: %s does not exist", target, targetRel)})
					continue
				}
			}
			if anchor == "" || !strings.HasSuffix(targetRel, ".md") {
				continue
			}
			td := d
			if targetRel != d.Path {
				var err error
				if td, err = parse(targetRel); err != nil {
					out = append(out, Problem{d.Path, pl.Line, fmt.Sprintf("link %q: cannot parse target: %v", target, err)})
					continue
				}
			}
			if !td.Headings[strings.ToLower(anchor)] {
				out = append(out, Problem{d.Path, pl.Line, fmt.Sprintf("broken anchor %q: no such heading in %s", target, targetRel)})
			}
		}
	}
	return out
}

// CheckPathRefs verifies that inline-code spans that look like repository
// paths (configs/..., app/..., docs/...) exist. Spans containing
// placeholders or globs (<, >, *, {, $) are ignored, as is a trailing
// ":line" suffix.
func CheckPathRefs(root string, d *Doc) []Problem {
	var out []Problem
	for _, pl := range d.Prose {
		for _, m := range inlineCode.FindAllStringSubmatch(pl.Text, -1) {
			ref := m[1]
			if i := strings.LastIndex(ref, ":"); i > 0 {
				if rest := ref[i+1:]; rest != "" && strings.Trim(rest, "0123456789-") == "" {
					ref = ref[:i]
				}
			}
			if !pathRef.MatchString(ref) {
				continue
			}
			ref = strings.TrimPrefix(ref, "./")
			// "pkg/path.Symbol" names a Go identifier inside the package.
			if i := strings.LastIndex(ref, "."); i > strings.LastIndex(ref, "/") && i+1 < len(ref) && ref[i+1] >= 'A' && ref[i+1] <= 'Z' {
				ref = ref[:i]
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				out = append(out, Problem{d.Path, pl.Line, fmt.Sprintf("referenced path %q does not exist", ref)})
			}
		}
	}
	return out
}

var mermaidKinds = []string{
	"flowchart", "graph", "sequenceDiagram", "stateDiagram-v2", "stateDiagram",
	"erDiagram", "classDiagram", "gantt", "journey", "timeline",
}

// CheckMermaid does a dependency-free sanity check of ```mermaid blocks:
// a known diagram type on the first line and balanced brackets/quotes.
// It is not a renderer; it catches the typos that break rendering most often.
func CheckMermaid(d *Doc) []Problem {
	var out []Problem
	for _, f := range d.Fences {
		if f.Lang != "mermaid" {
			continue
		}
		first := ""
		for _, l := range f.Body {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "%%") {
				first = t
				break
			}
		}
		ok := false
		for _, k := range mermaidKinds {
			if first == k || strings.HasPrefix(first, k+" ") {
				ok = true
			}
		}
		if !ok {
			out = append(out, Problem{d.Path, f.Line, fmt.Sprintf("mermaid block starts with %q, want a diagram type (flowchart, sequenceDiagram, stateDiagram-v2, ...)", first)})
			continue
		}
		stack := []rune{}
		pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
		inQuote := false
		for _, l := range f.Body {
			if strings.HasPrefix(strings.TrimSpace(l), "%%") {
				continue
			}
			for _, r := range l {
				switch {
				case r == '"':
					inQuote = !inQuote
				case inQuote:
				case r == '(' || r == '[' || r == '{':
					stack = append(stack, r)
				case r == ')' || r == ']' || r == '}':
					if len(stack) == 0 || stack[len(stack)-1] != pairs[r] {
						out = append(out, Problem{d.Path, f.Line, fmt.Sprintf("mermaid block: unbalanced %q", string(r))})
						stack = nil
					} else {
						stack = stack[:len(stack)-1]
					}
				}
			}
			// Quotes never span lines in Mermaid labels.
			if inQuote {
				out = append(out, Problem{d.Path, f.Line, "mermaid block: unterminated double quote"})
				inQuote = false
			}
		}
		if len(stack) > 0 {
			out = append(out, Problem{d.Path, f.Line, "mermaid block: unclosed bracket"})
		}
	}
	return out
}

// CheckYAML parses every ```yaml block. When strict is non-nil and the
// block's info string contains the word "settings", the block is also
// passed to strict, which should decode it strictly into the real
// configuration struct.
func CheckYAML(d *Doc, strict func(data []byte) error) []Problem {
	var out []Problem
	for _, f := range d.Fences {
		if f.Skip || (f.Lang != "yaml" && f.Lang != "yml") {
			continue
		}
		body := strings.Join(f.Body, "\n")
		var v any
		if err := yaml.Unmarshal([]byte(body), &v); err != nil {
			out = append(out, Problem{d.Path, f.Line, fmt.Sprintf("invalid YAML: %v", err)})
			continue
		}
		if strict != nil && strings.Contains(" "+f.Info+" ", " settings ") {
			if err := strict([]byte(body)); err != nil {
				out = append(out, Problem{d.Path, f.Line, fmt.Sprintf("settings snippet rejected by strict config decoding: %v", err)})
			}
		}
	}
	return out
}

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Telegram bot token", regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}\b`)},
	{"real-looking Telegram channel id", regexp.MustCompile(`-100\d{10,}`)},
	{"32-hex api_hash/secret", regexp.MustCompile(`(?i)(api_hash|secret|token|api_key)\s*[:=]\s*["']?[0-9a-f]{32}\b`)},
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"bearer/API key literal", regexp.MustCompile(`\b(sk|gsk|xox[bp])[-_][A-Za-z0-9_-]{20,}`)},
}

// placeholderChannelIDs are the documented fake channel IDs.
var placeholderChannelIDs = map[string]bool{
	"-1000000000000": true,
	"-1001234567890": true,
}

// CheckSecrets flags text that looks like a credential or a real channel ID.
func CheckSecrets(d *Doc) []Problem {
	var out []Problem
	for i, line := range d.Lines {
		for _, p := range secretPatterns {
			for _, m := range p.re.FindAllString(line, -1) {
				if p.name == "real-looking Telegram channel id" && placeholderChannelIDs[m] {
					continue
				}
				out = append(out, Problem{d.Path, i + 1, fmt.Sprintf("possible %s", p.name)})
			}
		}
	}
	return out
}
