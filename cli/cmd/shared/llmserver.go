package shared

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

// ListLLMModels lists the models an LLM server offers. It is a variable so
// tests can stub the live network call.
var ListLLMModels llmservers.ListFunc = llmservers.ListModels

const ownedModelAnnotation = "llm-server-owned"

// AddLLMServerFlags registers --llm-server and, if the command does not
// define it yet, --llm-model. Call it after the command's own flags.
func AddLLMServerFlags(cmd *cobra.Command) {
	cmd.Flags().String("llm-server", "", "use this llm_servers entry (by name) for this run; requires --llm-model")
	if cmd.Flags().Lookup("llm-model") == nil {
		cmd.Flags().String("llm-model", "", "model to use with --llm-server")
		// Only meaningful with --llm-server on commands that had no model
		// override of their own.
		_ = cmd.Flags().SetAnnotation("llm-model", ownedModelAnnotation, []string{"true"})
	}
}

// ApplyLLMServer returns cfg unchanged without --llm-server, else the
// selected copy after validating the flags and the live model list.
func ApplyLLMServer(cmd *cobra.Command, cfg *config.Config) (*config.Config, error) {
	name, _ := cmd.Flags().GetString("llm-server")
	if name == "" {
		if f := cmd.Flags().Lookup("llm-model"); f != nil && f.Changed && f.Annotations[ownedModelAnnotation] != nil {
			return nil, errors.New("--llm-model requires --llm-server")
		}
		return cfg, nil
	}
	model, _ := cmd.Flags().GetString("llm-model")
	if model == "" {
		return nil, errors.New("--llm-server requires --llm-model")
	}
	if baseURL, _ := cmd.Flags().GetString("llm-base-url"); baseURL != "" {
		return nil, errors.New("--llm-base-url cannot be combined with --llm-server")
	}
	server, err := llmservers.Find(cfg.LLMServers, name)
	if err != nil {
		return nil, err
	}
	if err := llmservers.Check(cmd.Context(), ListLLMModels, server, model); err != nil {
		return nil, err
	}
	return llmservers.Apply(cfg, server, model), nil
}
