package shellrunner_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/shellrunner"
)

func TestRun_ExecutesArgumentVectorWithoutShell(t *testing.T) {
	// A shell would expand $HOME and split on ';'; exec passes both through.
	out, err := shellrunner.Real{}.Run(context.Background(), "echo", "$HOME;", "x")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(out) != "$HOME; x" {
		t.Fatalf("output = %q, want the literal arguments", out)
	}
}

func TestRun_AbsolutePathIsAccepted(t *testing.T) {
	abs, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo not on PATH")
	}
	if _, err := (shellrunner.Real{}).Run(context.Background(), abs, "ok"); err != nil {
		t.Fatalf("Run(%s) error = %v", abs, err)
	}
}

func TestRun_NonZeroExitIsAnError(t *testing.T) {
	if _, err := (shellrunner.Real{}).Run(context.Background(), "false"); err == nil {
		t.Fatal("Run(false) error = nil")
	}
}

func TestRun_RejectsUnsafeCommandNames(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"", nil},
		{"../bin/echo", nil},
		{"../../usr/bin/env", nil},
		{"./script.sh", nil},
		{"scripts/deploy.sh", nil},
		{"/usr/bin/../bin/echo", nil},
		{"-rf", nil},
		{"/definitely/not/here/tool", nil},
		{"sh", []string{"-c", "echo hi"}},
		{"/bin/bash", []string{"-c", "echo hi"}},
		{"zsh", []string{"-lc", "echo hi"}},
	}
	for _, tc := range cases {
		_, err := shellrunner.Real{}.Run(context.Background(), tc.name, tc.args...)
		if !errors.Is(err, shellrunner.ErrUnsafeCommand) {
			t.Errorf("Run(%q, %q) err = %v, want ErrUnsafeCommand", tc.name, tc.args, err)
		}
	}
}
