package cli_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/cli"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
)

func TestRoot_ContainsGenericCommands(t *testing.T) {
	got := map[string]bool{}
	for _, c := range cli.Root().Commands() {
		got[c.Name()] = true
	}
	for _, name := range []string{"config", "audit", "train"} {
		if !got[name] {
			t.Errorf("Root() missing generic subcommand %q", name)
		}
	}
}

func TestRoot_ExcludesPrivateCommands(t *testing.T) {
	for _, path := range [][]string{
		{"deploy"}, {"telegram"}, {"dictionary", "fetch"}, {"dictionary", "update"}, {"backtest", "v1"},
	} {
		c, _, err := cli.Root().Find(path)
		if err == nil && c != nil && c.Name() == path[len(path)-1] && c != cli.Root() {
			t.Errorf("bare Root() contains private command %v", path)
		}
	}
}

func TestRoot_IsSingleton(t *testing.T) {
	first, second := cli.Root(), cli.Root()
	if first != second {
		t.Fatal("Root() returned different commands across calls, want the same singleton")
	}
}

func TestAdd_RegistersCommandsOnRoot(t *testing.T) {
	extra := &cobra.Command{Use: "zz-test-extra", RunE: func(*cobra.Command, []string) error { return nil }}
	cli.Add(extra)
	t.Cleanup(func() { cli.Root().RemoveCommand(extra) })

	found, _, err := cli.Root().Find([]string{"zz-test-extra"})
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if found != extra {
		t.Fatalf("Find() = %v, want the command registered via Add", found.Name())
	}
}

func TestProgramName_DefaultIsNeutral(t *testing.T) {
	if got := cli.ProgramName(); got != "phins-news-engine-tools" {
		t.Fatalf("ProgramName() = %q, want phins-news-engine-tools", got)
	}
	if got := cli.Root().Name(); got != "phins-news-engine-tools" {
		t.Fatalf("Root().Name() = %q, want phins-news-engine-tools", got)
	}
	if got := dictionaryupdate.ToolName(); got != "phins-news-engine-tools dictionary update" {
		t.Fatalf("dictionaryupdate.ToolName() = %q, want the neutral default", got)
	}
	if shared.ServiceBinaryName != "phins-news-engine" {
		t.Fatalf("shared.ServiceBinaryName = %q, want phins-news-engine", shared.ServiceBinaryName)
	}
	if shared.RemoteSyncScriptPath != "scripts/sync-remote-sqlite.sh" {
		t.Fatalf("shared.RemoteSyncScriptPath = %q, want scripts/sync-remote-sqlite.sh", shared.RemoteSyncScriptPath)
	}
}

func TestSetProgramName_OverridesEveryDerivedName(t *testing.T) {
	t.Cleanup(func() {
		cli.SetProgramName("")
		cli.SetServiceBinaryName("")
	})
	cli.SetProgramName("host-tools")
	cli.SetServiceBinaryName("host-service")
	if got := cli.Root().Name(); got != "host-tools" {
		t.Errorf("Root().Name() = %q, want host-tools", got)
	}
	if got := shared.ProgramName; got != "host-tools" {
		t.Errorf("shared.ProgramName = %q, want host-tools", got)
	}
	if got := dictionaryupdate.ToolName(); got != "host-tools dictionary update" {
		t.Errorf("dictionaryupdate.ToolName() = %q, want host-tools dictionary update", got)
	}
	if got := shared.ServiceBinaryName; got != "host-service" {
		t.Errorf("shared.ServiceBinaryName = %q, want host-service", got)
	}

	cli.SetProgramName("")
	cli.SetServiceBinaryName(" ")
	if cli.ProgramName() != shared.DefaultProgramName || cli.Root().Name() != shared.DefaultProgramName ||
		dictionaryupdate.ToolName() != dictionaryupdate.DefaultToolName || shared.ServiceBinaryName != shared.DefaultServiceBinaryName {
		t.Errorf("empty names must restore the defaults")
	}
}

func TestSetRemoteSyncScriptPath_OverridesAndRestoresDefault(t *testing.T) {
	t.Cleanup(func() { cli.SetRemoteSyncScriptPath("") })
	cli.SetRemoteSyncScriptPath("scripts/host-sync.sh")
	if got := shared.RemoteSyncScriptPath; got != "scripts/host-sync.sh" {
		t.Errorf("shared.RemoteSyncScriptPath = %q, want scripts/host-sync.sh", got)
	}
	cli.SetRemoteSyncScriptPath(" ")
	if got := shared.RemoteSyncScriptPath; got != shared.DefaultRemoteSyncScriptPath {
		t.Errorf("empty path must restore the default, got %q", got)
	}
}

func TestSetStatsLogPath_OverridesAndRestoresDefault(t *testing.T) {
	t.Cleanup(func() { cli.SetStatsLogPath("") })
	if shared.StatsLogPath != "data/logs/out.log" {
		t.Fatalf("default shared.StatsLogPath = %q, want data/logs/out.log", shared.StatsLogPath)
	}
	cli.SetStatsLogPath("data/host-logs/out.log")
	if got := shared.StatsLogPath; got != "data/host-logs/out.log" {
		t.Errorf("shared.StatsLogPath = %q, want data/host-logs/out.log", got)
	}
	cli.SetStatsLogPath(" ")
	if got := shared.StatsLogPath; got != shared.DefaultStatsLogPath {
		t.Errorf("empty path must restore the default, got %q", got)
	}
}

func TestSetDefaultDictionaryPath_UpdatesEveryDictionaryFlag(t *testing.T) {
	flags := func() map[string]string {
		got := map[string]string{}
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			// Flags with an empty default read dictionary.path from the config.
			if f := c.Flags().Lookup("dictionary"); f != nil && f.DefValue != "" {
				got[c.CommandPath()] = f.DefValue + "|" + f.Value.String()
			}
			for _, child := range c.Commands() {
				walk(child)
			}
		}
		walk(cli.Root())
		return got
	}
	before := flags()
	if len(before) == 0 {
		t.Fatal("no --dictionary flag found in the command tree")
	}
	want := shared.DefaultDictionaryPath + "|" + shared.DefaultDictionaryPath
	for path, v := range before {
		if v != want {
			t.Errorf("%s --dictionary = %q, want the neutral default %q", path, v, want)
		}
	}
	t.Cleanup(func() { cli.SetDefaultDictionaryPath("") })
	cli.SetDefaultDictionaryPath("configs/host_dictionary.yml")
	for path, v := range flags() {
		if v != "configs/host_dictionary.yml|configs/host_dictionary.yml" {
			t.Errorf("%s --dictionary = %q after override", path, v)
		}
	}
}
