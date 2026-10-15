// Package shellrunner provides the real (non-fake) implementation of
// application/deployment.CommandRunner, wrapping os/exec.CommandContext.
// Matches infrastructure/clock.Real{}'s "one real struct, tests fake the
// interface" pattern.
//
// Commands run as an argument vector, never through a shell: arguments
// are passed to the program verbatim, with no expansion or word
// splitting. The program name is validated before anything runs (see
// validateName).
package shellrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUnsafeCommand reports a program name or shell invocation that Run
// refuses to execute.
var ErrUnsafeCommand = errors.New("shellrunner: unsafe command")

// shells are refused when asked to run a command string (-c), which would
// reintroduce shell parsing of its argument.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

type Real struct{}

// Run executes name with args, returning combined stdout+stderr
// (tail-truncated by the caller if needed — this function returns
// everything) and a wrapped error on non-zero exit. name must pass
// validateName; otherwise nothing runs and the error matches
// ErrUnsafeCommand.
func (Real) Run(ctx context.Context, name string, args ...string) (string, error) {
	if err := validateName(name, args); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- argument vector only (no shell); name validated by validateName and supplied by application/deployment.Run's fixed callers ("make", "git", "env")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("shellrunner: %s %v: %w", name, args, err)
	}
	return out.String(), nil
}

// validateName accepts either a bare program name resolved through PATH
// ("git", "make") or a clean absolute path to an existing regular file.
// It rejects an empty name, a name starting with "-", any relative path
// (which would resolve against an unexpected working directory), any ".."
// element, and a shell asked to run a command string.
func validateName(name string, args []string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty program name", ErrUnsafeCommand)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("%w: program name %q looks like a flag", ErrUnsafeCommand, name)
	}
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if part == ".." {
			return fmt.Errorf("%w: %q escapes its directory", ErrUnsafeCommand, name)
		}
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) || filepath.Clean(name) != name {
			return fmt.Errorf("%w: %q must be a bare program name or a clean absolute path", ErrUnsafeCommand, name)
		}
		fi, err := os.Stat(name)
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is not an existing file", ErrUnsafeCommand, name)
		}
	}
	if shells[filepath.Base(name)] {
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
				return fmt.Errorf("%w: %s with a command string", ErrUnsafeCommand, filepath.Base(name))
			}
		}
	}
	return nil
}
