// cli/cmd/entities/cycle.go
package entities

import (
	"bufio"
	"database/sql"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Verification modes of the cycle's entity step.
const (
	CycleVerificationLocal = entitywiring.CycleVerificationLocal
	CycleVerificationFull  = entitywiring.CycleVerificationFull
	CycleVerificationNone  = entitywiring.CycleVerificationNone
)

// CycleOptions configures RunCycleStep.
type CycleOptions struct{ Verification string }

// ValidateCycleVerification checks an --entities-verification value.
func ValidateCycleVerification(v string) error { return entitywiring.ValidateCycleVerification(v) }

// NewCycleLLM builds the discovery LLM; a variable so tests can substitute it.
var NewCycleLLM = func(cmd *cobra.Command, cfg config.EntityDiscoveryConfig, maxTokens int, disableThinking bool) appout.EntityDiscoveryLLM {
	return entitywiring.NewDiscoveryLLM(cfg, maxTokens, disableThinking, discoveryLogger{out: cmd.OutOrStderr(), component: "llm"})
}

// CycleStepDisabledReason returns why the entity step cannot run with cfg, or ""
// when it can.
func CycleStepDisabledReason(cfg *config.Config) string {
	switch {
	case !cfg.EntityDiscovery.Enabled:
		return "entity_discovery.enabled is false"
	case cfg.Clustering.EntityCatalog.ManualPath == "" || cfg.Clustering.EntityCatalog.GeneratedPath == "":
		return "clustering.entity_catalog paths are not configured"
	case cfg.Dictionary.Path == "":
		return "dictionary.path is empty"
	}
	return ""
}

// RunCycleStep runs the entity step of `editorial cycle` for one session.
func RunCycleStep(cmd *cobra.Command, cfg *config.Config, db *sql.DB, sessionID string, scanner *bufio.Scanner, opts CycleOptions) (entitycycle.Result, error) {
	var zero entitycycle.Result
	newLLM := func(dc config.EntityDiscoveryConfig, maxTokens int, disableThinking bool, _ appout.Logger) appout.EntityDiscoveryLLM {
		return NewCycleLLM(cmd, dc, maxTokens, disableThinking)
	}
	deps, in, err := entitywiring.CycleDeps(cmd.Context(), cfg, db, sessionID, opts.Verification, cmd.OutOrStderr(), newLLM)
	if err != nil {
		return zero, err
	}
	if len(in.SessionArticleIDs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "entities: the session has no reviewed articles")
		return zero, nil
	}
	in.Scanner, in.Out = scanner, cmd.OutOrStdout()
	return entitycycle.Run(cmd.Context(), deps, in)
}
