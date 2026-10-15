package docscheck

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CLI describes what documented commands are validated against. The
// library ships no service binary (a host calls engine.Run), so only the
// tools command tree and the Makefile are checked. The tools executable
// is recognised by the root command's name.
type CLI struct {
	Tools       *cobra.Command // tools root, as returned by cli.Root()
	MakeTargets map[string]bool
}

var (
	makeTargetRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)\s*:($|[^=])`)
	envAssignRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// MakeTargets returns the explicit targets declared in the Makefile.
func MakeTargets(root string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "Makefile")) // #nosec G304 -- fixed repo path
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		if m := makeTargetRe.FindStringSubmatch(l); m != nil {
			out[m[1]] = true
		}
	}
	return out, nil
}

// CheckCommands validates documented shell commands in unskipped
// bash/sh/console blocks, and inline-code spans that start with a known
// executable.
func CheckCommands(d *Doc, cli CLI) []Problem {
	var out []Problem
	for _, f := range d.Fences {
		if f.Skip {
			continue
		}
		switch f.Lang {
		case "bash", "sh", "shell", "console", "zsh":
		default:
			continue
		}
		for _, l := range logicalLines(f.Body) {
			out = append(out, checkLine(d.Path, f.Line, l, cli)...)
		}
	}
	for _, pl := range d.Prose {
		for _, m := range inlineCode.FindAllStringSubmatch(pl.Text, -1) {
			span := strings.TrimSpace(m[1])
			if strings.HasPrefix(span, "make ") || (cli.Tools != nil && strings.Contains(span, cli.Tools.Name()+" ")) {
				out = append(out, checkLine(d.Path, pl.Line, span, cli)...)
			}
		}
	}
	return out
}

// logicalLines joins backslash continuations and drops comments/blank lines
// and heredoc-free noise.
func logicalLines(body []string) []string {
	var out []string
	var cur strings.Builder
	for _, l := range body {
		t := strings.TrimSpace(l)
		if cur.Len() == 0 && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur.WriteString(strings.TrimSuffix(t, "\\"))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(t)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// tokenize splits on whitespace honoring quotes, and splits shell control
// operators into their own tokens.
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		case r == '#' && cur.Len() == 0:
			flush()
			return toks
		case r == '|' || r == ';' || r == '&' || r == '>' || r == '<':
			flush()
			op := string(r)
			if i+1 < len(rs) && (rs[i+1] == r) {
				op += string(r)
				i++
			}
			toks = append(toks, "\x00"+op)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}

func checkLine(file string, line int, cmdline string, cli CLI) []Problem {
	var out []Problem
	cmdline = strings.TrimPrefix(cmdline, "$ ")
	var seg []string
	flush := func() {
		if len(seg) > 0 {
			out = append(out, checkSegment(file, line, seg, cli)...)
		}
		seg = nil
	}
	for _, t := range tokenize(cmdline) {
		if strings.HasPrefix(t, "\x00") {
			// Redirections consume the next token as a file name; treating
			// the operator as a segment break is enough because file names
			// never resolve to a known executable.
			flush()
			continue
		}
		seg = append(seg, t)
	}
	flush()
	return out
}

func checkSegment(file string, line int, toks []string, cli CLI) []Problem {
	i := 0
	for i < len(toks) && (envAssignRe.MatchString(toks[i]) || toks[i] == "sudo" || toks[i] == "time" || toks[i] == "exec") {
		i++
	}
	if i >= len(toks) {
		return nil
	}
	exe := toks[i]
	args := toks[i+1:]
	switch {
	case exe == "make":
		return checkMake(file, line, args, cli)
	case cli.Tools != nil && path.Base(exe) == cli.Tools.Name():
		return checkTools(file, line, args, cli)
	}
	return nil
}

func checkMake(file string, line int, args []string, cli CLI) []Problem {
	var out []Problem
	for _, a := range args {
		if strings.HasPrefix(a, "-") || envAssignRe.MatchString(a) || strings.ContainsAny(a, "<>$[") {
			continue
		}
		if !cli.MakeTargets[a] {
			out = append(out, Problem{file, line, fmt.Sprintf("make target %q does not exist in the Makefile", a)})
		}
	}
	return out
}

func checkTools(file string, line int, args []string, cli CLI) []Problem {
	var out []Problem
	cur := cli.Tools
	descending := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "..." || strings.ContainsAny(a, "<[") {
			// Placeholder such as <article-id> or [flags]: stop validating.
			break
		}
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			name, _, hasVal := strings.Cut(name, "=")
			if name == "h" || name == "help" {
				continue
			}
			fl := lookupFlag(cur, name, strings.HasPrefix(a, "--"))
			if fl == nil {
				out = append(out, Problem{file, line, fmt.Sprintf("%q has no flag %q", cur.CommandPath(), a)})
				continue
			}
			if !hasVal && fl.NoOptDefVal == "" {
				i++ // value-taking flag consumes the next token
			}
			continue
		}
		if descending {
			if next := findChild(cur, a); next != nil {
				cur = next
				continue
			}
			if cur.HasSubCommands() && !cur.Runnable() {
				out = append(out, Problem{file, line, fmt.Sprintf("%q has no subcommand %q", cur.CommandPath(), a)})
				break
			}
			descending = false
		}
	}
	return out
}

func findChild(c *cobra.Command, name string) *cobra.Command {
	for _, ch := range c.Commands() {
		if ch.Name() == name || ch.HasAlias(name) {
			return ch
		}
	}
	return nil
}

func lookupFlag(c *cobra.Command, name string, long bool) *pflag.Flag {
	for _, fs := range []*pflag.FlagSet{c.LocalFlags(), c.InheritedFlags(), c.PersistentFlags(), c.Flags()} {
		if long || len(name) > 1 {
			if f := fs.Lookup(name); f != nil {
				return f
			}
		} else if f := fs.ShorthandLookup(name); f != nil {
			return f
		}
	}
	return nil
}
