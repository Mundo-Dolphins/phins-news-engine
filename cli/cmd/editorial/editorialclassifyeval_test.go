// cli/cmd/editorial/editorialclassifyeval_test.go
package editorial

import (
	"testing"
)

func TestNewClassifyEvalCommand_HasExpectedFlagsAndDefaults(t *testing.T) {
	cmd := newClassifyEvalCommand()
	for _, name := range []string{"dataset", "fold", "sample", "seed", "threshold", "batch-size", "policy-file", "misses-out", "include-imported", "llm-base-url", "llm-model"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
	if fold, _ := cmd.Flags().GetString("fold"); fold != "dev" {
		t.Errorf("fold default = %q, want dev (holdout must be an explicit choice)", fold)
	}
	if imported, _ := cmd.Flags().GetBool("include-imported"); imported {
		t.Error("include-imported default = true, want false: telegramimport labels are not human-reviewed")
	}
}
