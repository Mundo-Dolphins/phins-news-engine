// Package cli wires the tools Cobra command tree. Host programs execute it
// via Execute, may extend it with Add and may rename it with SetProgramName.
package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/audit"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/backtest"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/clustering"
	configcmd "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/config"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/dedup"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/entities"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/events"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/label"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/model"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/seenitems"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/sourceid"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/sourcepolicy"
	sqlitecmd "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/stats"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/train"
)

// root is the tools root command singleton.
var root = &cobra.Command{
	Use:           shared.DefaultProgramName,
	Short:         "Offline tooling for the news engine: config, audit, editorial review, training, SQLite maintenance.",
	SilenceUsage:  true,
	SilenceErrors: false,
}

func init() {
	root.PersistentFlags().StringVar(&shared.ConfigPath, "config", "configs/settings.yml", "path to the YAML configuration file")
	root.PersistentFlags().StringVar(&shared.ProfileFlag, "profile", "", "deployment profile: local (default), test, or production")

	root.AddCommand(configcmd.NewCommand())
	root.AddCommand(sqlitecmd.NewCommand())
	root.AddCommand(audit.NewCommand())
	root.AddCommand(editorial.NewReviewCommand())
	root.AddCommand(editorial.NewCommand())
	root.AddCommand(label.NewCommand())
	root.AddCommand(train.NewCommand())
	root.AddCommand(model.NewValidateCommand())
	root.AddCommand(dictionary.NewCommand())
	root.AddCommand(entities.NewCommand())
	root.AddCommand(events.NewCommand())
	root.AddCommand(dedup.NewCommand())
	root.AddCommand(clustering.NewCommand())
	root.AddCommand(sourcepolicy.NewCommand())
	root.AddCommand(sourceid.NewCommand())
	root.AddCommand(seenitems.NewCommand())
	root.AddCommand(backtest.NewCommand())
	root.AddCommand(stats.NewCommand())
}

// SetProgramName renames the tools CLI: the root command's Use name, the
// command hints printed to the operator, the tools binary file name the
// editorial cycle deploys and the Tool value stamped on dictionary versions
// ("<name> dictionary update") all derive from it. An empty name restores
// shared.DefaultProgramName.
func SetProgramName(name string) {
	if strings.TrimSpace(name) == "" {
		name = shared.DefaultProgramName
	}
	shared.ProgramName = name
	root.Use = name
	dictionaryupdate.SetToolName(name + " dictionary update")
}

// ProgramName returns the tools CLI's current name.
func ProgramName() string { return shared.ProgramName }

// SetServiceBinaryName sets the service binary file name (under build/) the
// editorial cycle deploys. An empty name restores
// shared.DefaultServiceBinaryName.
func SetServiceBinaryName(name string) {
	if strings.TrimSpace(name) == "" {
		name = shared.DefaultServiceBinaryName
	}
	shared.ServiceBinaryName = name
}

// SetRemoteSyncScriptPath sets the remote database sync script (relative to
// the tools binary's working directory) that the editorial cycle's sync step
// runs. An empty path restores shared.DefaultRemoteSyncScriptPath.
func SetRemoteSyncScriptPath(path string) {
	if strings.TrimSpace(path) == "" {
		path = shared.DefaultRemoteSyncScriptPath
	}
	shared.RemoteSyncScriptPath = path
}

// SetStatsLogPath sets the process log that `stats` scans for cycle activity
// when --log-path is not given. An empty path restores
// shared.DefaultStatsLogPath.
func SetStatsLogPath(path string) {
	if strings.TrimSpace(path) == "" {
		path = shared.DefaultStatsLogPath
	}
	shared.StatsLogPath = path
}

// SetDefaultDictionaryPath changes the default of every --dictionary flag
// in the command tree (including commands added with Add before the call)
// that still carries the library default. An empty path restores
// shared.DefaultDictionaryPath.
func SetDefaultDictionaryPath(path string) {
	if strings.TrimSpace(path) == "" {
		path = shared.DefaultDictionaryPath
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if f := c.Flags().Lookup("dictionary"); f != nil && f.DefValue == currentDictionaryDefault {
			f.DefValue = path
			if !f.Changed {
				_ = f.Value.Set(path)
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)
	currentDictionaryDefault = path
}

// currentDictionaryDefault is the default --dictionary flags carry now.
var currentDictionaryDefault = shared.DefaultDictionaryPath

// Root returns the tools root command singleton.
func Root() *cobra.Command {
	return root
}

// Add registers additional commands (e.g. private, host-specific ones) on
// the root command.
func Add(cmds ...*cobra.Command) {
	root.AddCommand(cmds...)
}

// Execute runs the root command.
func Execute() error {
	return root.Execute()
}
