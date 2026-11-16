// Package llm holds the `llm` command group: inspection of the configured
// selectable LLM servers.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

// NewCommand builds the `llm` command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "Inspect the configured LLM servers",
	}
	cmd.AddCommand(newModelsCommand())
	return cmd
}

type result struct {
	models []string
	err    error
}

func newModelsCommand() *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models each configured LLM server offers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			servers := cfg.LLMServers
			if server != "" {
				s, err := llmservers.Find(servers, server)
				if err != nil {
					return err
				}
				servers = []config.LLMServerConfig{s}
			} else if len(servers) == 0 {
				return errors.New("no llm_servers configured")
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			results := make([]result, len(servers))
			var wg sync.WaitGroup
			for i, s := range servers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i].models, results[i].err = shared.ListLLMModels(ctx, s)
				}()
			}
			wg.Wait()

			out := cmd.OutOrStdout()
			failed := 0
			for i, s := range servers {
				fmt.Fprintf(out, "%s  %s\n", s.Name, s.BaseURL)
				if results[i].err != nil {
					failed++
					fmt.Fprintf(out, "  unavailable: %v\n", results[i].err)
					continue
				}
				for _, m := range results[i].models {
					fmt.Fprintf(out, "  %s\n", m)
				}
			}
			if failed == len(servers) {
				return errors.New("no llm server is reachable")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "llm-server", "", "only list this configured server")
	return cmd
}
