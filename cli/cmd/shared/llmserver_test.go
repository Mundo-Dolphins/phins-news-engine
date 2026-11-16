package shared_test

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func stubList(t *testing.T, models ...string) *int {
	t.Helper()
	orig := shared.ListLLMModels
	t.Cleanup(func() { shared.ListLLMModels = orig })
	calls := 0
	shared.ListLLMModels = func(context.Context, config.LLMServerConfig) ([]string, error) {
		calls++
		return models, nil
	}
	return &calls
}

func cfgWithServers() *config.Config {
	return &config.Config{LLMServers: []config.LLMServerConfig{
		{Name: "server-a", BaseURL: "http://server-a:8000/v1"},
		{Name: "server-b", BaseURL: "http://server-b:11434/v1"},
	}}
}

func newCmd(predefine bool) *cobra.Command {
	cmd := &cobra.Command{Use: "x"}
	if predefine {
		cmd.Flags().String("llm-model", "", "")
		cmd.Flags().String("llm-base-url", "", "")
	}
	shared.AddLLMServerFlags(cmd)
	return cmd
}

func TestApplyLLMServer(t *testing.T) {
	tests := []struct {
		name       string
		predefine  bool
		args       []string
		wantErr    string
		wantSame   bool
		wantURL    string
		wantModel  string
		wantListed int
	}{
		{name: "no flags", args: nil, wantSame: true},
		{name: "model alone on a command with its own --llm-model", predefine: true, args: []string{"--llm-model", "m1"}, wantSame: true},
		{name: "model alone on a command whose --llm-model the helper added", args: []string{"--llm-model", "m1"}, wantErr: "--llm-model requires --llm-server"},
		{name: "server without model", args: []string{"--llm-server", "server-a"}, wantErr: "--llm-server requires --llm-model"},
		{name: "selected", args: []string{"--llm-server", "server-a", "--llm-model", "m1"}, wantURL: "http://server-a:8000/v1", wantModel: "m1", wantListed: 1},
		{name: "selected predefined flags", predefine: true, args: []string{"--llm-server", "server-b", "--llm-model", "m1"}, wantURL: "http://server-b:11434/v1", wantModel: "m1", wantListed: 1},
		{name: "model not listed", args: []string{"--llm-server", "server-a", "--llm-model", "nope"}, wantErr: `model "nope" is not available on server-a`, wantListed: 1},
		{name: "unknown server", args: []string{"--llm-server", "zzz", "--llm-model", "m1"}, wantErr: `unknown llm server "zzz"`},
		{name: "base url conflict", predefine: true, args: []string{"--llm-server", "server-a", "--llm-model", "m1", "--llm-base-url", "http://x/v1"}, wantErr: "--llm-base-url cannot be combined with --llm-server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubList(t, "m1")
			cmd := newCmd(tt.predefine)
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			cfg := cfgWithServers()
			got, err := shared.ApplyLLMServer(cmd, cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if *calls != tt.wantListed {
				t.Errorf("list calls = %d, want %d", *calls, tt.wantListed)
			}
			if tt.wantSame && got != cfg {
				t.Error("want same cfg pointer")
			}
			if tt.wantURL != "" {
				if got == cfg {
					t.Fatal("want a copy")
				}
				o, e := got.OfflineTraining.LLM, got.EntityDiscovery.LLM
				if o.BaseURL != tt.wantURL || o.Model != tt.wantModel || e.BaseURL != tt.wantURL || e.Model != tt.wantModel {
					t.Errorf("offline = %q %q, entity = %q %q, want %q %q", o.BaseURL, o.Model, e.BaseURL, e.Model, tt.wantURL, tt.wantModel)
				}
			}
		})
	}
}

func TestAddLLMServerFlags_KeepsExistingModelFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("llm-model", "", "own usage")
	shared.AddLLMServerFlags(cmd)
	if got := cmd.Flags().Lookup("llm-model").Usage; got != "own usage" {
		t.Errorf("llm-model usage = %q, existing flag replaced", got)
	}
	if cmd.Flags().Lookup("llm-server") == nil {
		t.Error("llm-server not registered")
	}
}
