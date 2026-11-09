// cli/cmd/entities/verifier_build.go
package entities

import (
	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// addVerificationFlags registers the verification stage flags.
func addVerificationFlags(cmd *cobra.Command, f *entitywiring.Stages) {
	cmd.Flags().BoolVar(&f.Wikidata, "wikidata", false, "verify candidates against Wikidata first (overrides entity_discovery.verification.wikidata_first)")
	cmd.Flags().BoolVar(&f.NoWikidata, "no-wikidata", false, "disable the Wikidata-first stage for this run")
	cmd.Flags().BoolVar(&f.Fallback, "fallback", false, "enable the LLM + SearXNG fallback stage for this run")
	cmd.Flags().BoolVar(&f.NoFallback, "no-fallback", false, "disable the LLM + SearXNG fallback stage for this run")
	cmd.Flags().BoolVar(&f.Offline, "offline", false, "no network verification at all (disables both stages)")
	cmd.Flags().BoolVar(&f.NoLLM, "no-llm", false, "do not call the LLM chooser for ambiguous league mentions in this run")
	cmd.MarkFlagsMutuallyExclusive("wikidata", "no-wikidata")
	cmd.MarkFlagsMutuallyExclusive("fallback", "no-fallback")
}

// cliLoggers tags each wiring component's log lines on the command's stderr.
func cliLoggers(cmd *cobra.Command) entitywiring.Loggers {
	return func(c string) appout.Logger { return discoveryLogger{out: cmd.OutOrStderr(), component: c} }
}

// llmOverrides turns the --llm-* flag values into wiring overrides; the
// numeric and boolean ones apply only when the flag was given.
func llmOverrides(cmd *cobra.Command, baseURL, model string, maxTokens int, disableThinking bool) entitywiring.LLMOverrides {
	o := entitywiring.LLMOverrides{BaseURL: baseURL, Model: model}
	if cmd.Flags().Changed("llm-max-tokens") {
		o.MaxTokens = &maxTokens
	}
	if cmd.Flags().Changed("llm-disable-thinking") {
		o.DisableThinking = &disableThinking
	}
	return o
}
