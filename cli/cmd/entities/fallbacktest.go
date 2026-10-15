// cli/cmd/entities/fallbacktest.go
package entities

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	domainresolution "github.com/Mundo-Dolphins/phins-news-engine/domain/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func newFallbackTestCommand() *cobra.Command {
	var candidate, contextText, field string

	cmd := &cobra.Command{
		Use:   "fallback-test",
		Short: "Send one candidate through the configured online entity resolution fallback and print the result",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("entities fallback-test: %w", err)
			}
			fb := cfg.EntityExtraction.Fallback
			if !fb.Enabled {
				return fmt.Errorf("entities fallback-test: entity_extraction.fallback.enabled is false in %s", shared.ConfigPath)
			}

			resolver := llmclient.NewResolver(llmclient.Config{
				BaseURL: fb.BaseURL, APIKey: fb.APIKey, Model: fb.Model,
				Temperature: fb.Temperature, MaxTokens: fb.MaxTokens, Timeout: fb.Timeout(),
			})

			ctx, cancel := context.WithTimeout(cmd.Context(), fb.Timeout())
			defer cancel()
			return runFallbackTest(ctx, cmd.OutOrStdout(), resolver, fb, candidate, contextText, field)
		},
	}
	cmd.Flags().StringVar(&candidate, "candidate", "", "candidate text to resolve (required)")
	cmd.Flags().StringVar(&contextText, "context", "", "surrounding context text the candidate appeared in (required)")
	cmd.Flags().StringVar(&field, "field", "title", "field the candidate was found in: title|description|body")
	_ = cmd.MarkFlagRequired("candidate")
	_ = cmd.MarkFlagRequired("context")
	return cmd
}

// runFallbackTest sends one candidate through resolver, runs the same
// local validation the online fallback applies in production
// (domainresolution.Validate), and writes a human-readable report to
// out. Separated from newFallbackTestCommand's RunE so tests can inject
// a fake appout.EntityResolutionLLM without a config file or network
// call — this command never opens the SQLite cache, unlike the real
// fallback path (app/entityresolution.Resolve): it's a
// prompt/provider diagnostic, not a pipeline simulator.
func runFallbackTest(ctx context.Context, out io.Writer, resolver appout.EntityResolutionLLM, fb config.EntityFallbackConfig, candidate, contextText, field string) error {
	normalized := entityproposal.Normalize(candidate)
	result, err := resolver.Resolve(ctx, appout.EntityResolutionCandidate{
		Text: candidate, NormalizedText: normalized, Field: field, Context: contextText,
	})
	provider := fb.Provider
	if provider == "" {
		provider = "unknown"
	}
	if err != nil {
		fmt.Fprintf(out, "Provider: %s\nError: %v\n", provider, err)
		return nil
	}

	validateErr := domainresolution.Validate(
		domainresolution.Candidate{Text: candidate, NormalizedText: normalized, Field: field, Context: contextText},
		domainresolution.Resolution{Resolved: result.Resolved, Type: result.Type, CanonicalName: result.CanonicalName, Confidence: result.Confidence},
		fb.MinConfidence,
	)

	fmt.Fprintf(out, "Provider: %s\nResolved: %v\nType: %s\nCanonical name: %s\nConfidence: %.2f\n",
		provider, result.Resolved, result.Type, result.CanonicalName, result.Confidence)
	switch {
	case validateErr != nil:
		fmt.Fprintf(out, "Local validation: REJECTED (%v)\n", validateErr)
	case result.Resolved:
		fmt.Fprintln(out, "Local validation: accepted")
	}
	return nil
}
