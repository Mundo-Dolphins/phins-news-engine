package editorial

import "github.com/spf13/cobra"

func NewCommand() *cobra.Command {
	editorialCmd := &cobra.Command{Use: "editorial", Short: "End-to-end interactive editorial cycle"}
	editorialCmd.AddCommand(newCycleCommand())
	editorialCmd.AddCommand(newMetricsCommand())
	editorialCmd.AddCommand(newCanaryCommand())
	editorialCmd.AddCommand(newClassifyCommand())
	editorialCmd.AddCommand(newClassifyEvalCommand())
	editorialCmd.AddCommand(newVetoReportCommand())
	return editorialCmd
}
