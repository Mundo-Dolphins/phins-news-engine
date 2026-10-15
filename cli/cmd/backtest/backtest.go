package backtest

import "github.com/spf13/cobra"

// NewCommand returns the public backtest group. The private "v1" subcommand
// is attached by the host program.
func NewCommand() *cobra.Command {
	backtestCmd := &cobra.Command{Use: "backtest", Short: "Replay historical article traffic through v2's current pipeline"}
	backtestCmd.AddCommand(newCandidateCommand())
	backtestCmd.AddCommand(newLeagueCommand())
	return backtestCmd
}
