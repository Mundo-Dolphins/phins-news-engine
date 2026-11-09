package entities

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

func TestVerificationFlags_ContradictoryPairsAreMutuallyExclusive(t *testing.T) {
	for _, args := range [][]string{{"--wikidata", "--no-wikidata"}, {"--fallback", "--no-fallback"}} {
		var f entitywiring.Stages
		cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		addVerificationFlags(cmd, &f)
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "none of the others can be") {
			t.Errorf("%v: err = %v, want mutually exclusive error", args, err)
		}
	}
}

func TestVerificationFlags_NoLLMFlagIsRegistered(t *testing.T) {
	var f entitywiring.Stages
	cmd := &cobra.Command{Use: "x"}
	addVerificationFlags(cmd, &f)
	if err := cmd.ParseFlags([]string{"--no-llm"}); err != nil || !f.NoLLM {
		t.Fatalf("--no-llm: err=%v noLLM=%v", err, f.NoLLM)
	}
}
