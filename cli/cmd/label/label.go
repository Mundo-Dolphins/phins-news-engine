package label

import (
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func NewCommand() *cobra.Command {
	labelCmd := &cobra.Command{Use: "label", Short: "Manage human labels for editorial classifier training"}
	labelCmd.AddCommand(newSetCommand())
	labelCmd.AddCommand(newHistoryCommand())
	labelCmd.AddCommand(newConflictsCommand())
	labelCmd.AddCommand(newResolveConflictCommand())
	return labelCmd
}

func openLabelRepo(cmd *cobra.Command) (*sqliteadapter.HumanLabelRepository, func(), error) {
	labels, _, closeDB, err := openLabelRepos(cmd)
	return labels, closeDB, err
}

// openLabelRepos additionally returns the hard-example repository, which
// `label set` uses to mark a mined candidate resolved once a human has
// applied a label to it.
func openLabelRepos(cmd *cobra.Command) (*sqliteadapter.HumanLabelRepository, *sqliteadapter.HardExampleRepository, func(), error) {
	cfg, err := config.Load(shared.ConfigPath)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return nil, nil, nil, err
	}
	if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return sqliteadapter.NewHumanLabelRepository(db), sqliteadapter.NewHardExampleRepository(db), func() { _ = db.Close() }, nil
}

// resolveHardExample closes the hard-example mining loop: migration 0039 and
// training.HardExampleCandidate both
// promise that "a human resolving the article (any label) marks it
// resolved via resolved_at", and retraintrigger's MinHardExampleCount
// reads UnresolvedCount -- so without this the count could only ever
// grow and the trigger would latch on forever once enabled.
//
// Best-effort by design: most labeled articles were never mined, and
// MarkResolved is a documented no-op (not an error) for an article with
// no candidate row, so a failure here warns rather than failing a label
// the repository already accepted.
func resolveHardExample(cmd *cobra.Command, hardExamples *sqliteadapter.HardExampleRepository, articleID string) {
	if err := hardExamples.MarkResolved(cmd.Context(), articleID, time.Now().UTC()); err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "warning: failed to mark hard-example candidate for %s resolved: %v\n", articleID, err)
	}
}

func newSetCommand() *cobra.Command {
	var by, notes string
	setCmd := &cobra.Command{
		Use:   "set <article-id> <publish|reject>",
		Short: "Human-label an article for editorial classifier training",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			articleID, label := args[0], args[1]
			if label != "publish" && label != "reject" {
				return fmt.Errorf("label: invalid label %q (want \"publish\" or \"reject\")", label)
			}

			labels, hardExamples, closeDB, err := openLabelRepos(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			example := training.Example{
				ArticleID: articleID, Label: label, LabeledBy: by, LabeledAt: time.Now().UTC(), Notes: notes, Source: "cli:label",
			}

			active, hasActive, err := labels.GetActive(cmd.Context(), articleID)
			if err != nil {
				return fmt.Errorf("label: check existing label for %s: %w", articleID, err)
			}
			if hasActive {
				if err := labels.Relabel(cmd.Context(), example, active.Revision); err != nil {
					return fmt.Errorf("label: relabel %s: %w", articleID, err)
				}
				resolveHardExample(cmd, hardExamples, articleID)
				cmd.Printf("relabeled %s as %s (was %s, now revision %d)\n", articleID, label, active.Label, active.Revision+1)
				return nil
			}
			// No active label can also mean "this article has an
			// unresolved legacy conflict" -- migration 0026 leaves every
			// row of an ambiguous group inactive. Labeling straight
			// through would orphan the conflict row (never marked
			// resolved), leaving `train export`'s "N article(s) excluded"
			// warning permanently wrong about an article that now has a
			// perfectly valid active label. Send the operator to
			// resolve-conflict, which does both halves atomically.
			conflicts, err := labels.ListUnresolvedConflicts(cmd.Context())
			if err != nil {
				return fmt.Errorf("label: check unresolved conflicts for %s: %w", articleID, err)
			}
			for _, c := range conflicts {
				if c.ArticleID == articleID {
					return fmt.Errorf("label: %s has an unresolved legacy label conflict -- use \"label resolve-conflict %s %s\" instead of \"label set\"", articleID, articleID, label)
				}
			}
			if err := labels.Label(cmd.Context(), example); err != nil {
				return err
			}
			resolveHardExample(cmd, hardExamples, articleID)
			cmd.Printf("labeled %s as %s\n", articleID, label)
			return nil
		},
	}
	setCmd.Flags().StringVar(&by, "by", "", "who applied this label")
	setCmd.Flags().StringVar(&notes, "notes", "", "free-text notes for this label")
	return setCmd
}

func newHistoryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "history <article-id>",
		Short: "Show every label revision for an article, active and superseded",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			articleID := args[0]
			labels, closeDB, err := openLabelRepo(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			history, err := labels.ListByArticle(cmd.Context(), articleID)
			if err != nil {
				return err
			}
			if len(history) == 0 {
				cmd.Printf("no label history for %s\n", articleID)
				return nil
			}
			active, hasActive, err := labels.GetActive(cmd.Context(), articleID)
			if err != nil {
				return err
			}
			for _, e := range history {
				status := "superseded"
				if hasActive && active.Revision == e.Revision {
					status = "active"
				}
				cmd.Printf("rev %d [%s] %s by %q at %s (source=%s reason=%q)\n", e.Revision, status, e.Label, e.LabeledBy, e.LabeledAt.Format(time.RFC3339), e.Source, e.Reason)
			}
			return nil
		},
	}
}

func newConflictsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "conflicts",
		Short: "List article_ids with an unresolved legacy label conflict",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			labels, closeDB, err := openLabelRepo(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			conflicts, err := labels.ListUnresolvedConflicts(cmd.Context())
			if err != nil {
				return err
			}
			if len(conflicts) == 0 {
				cmd.Println("no unresolved label conflicts")
				return nil
			}
			for _, c := range conflicts {
				cmd.Printf("%s [%s]: %s\n", c.ArticleID, c.TableName, c.Detail)
			}
			return nil
		},
	}
}

func newResolveConflictCommand() *cobra.Command {
	var by, notes string
	resolveCmd := &cobra.Command{
		Use:   "resolve-conflict <article-id> <publish|reject>",
		Short: "Manually resolve an unresolved legacy label conflict by choosing the winning label",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			articleID, label := args[0], args[1]
			if label != "publish" && label != "reject" {
				return fmt.Errorf("label resolve-conflict: invalid label %q (want \"publish\" or \"reject\")", label)
			}
			labels, hardExamples, closeDB, err := openLabelRepos(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			if err := labels.ResolveConflict(cmd.Context(), articleID, training.Example{
				ArticleID: articleID, Label: label, LabeledBy: by, LabeledAt: time.Now().UTC(), Notes: notes, Source: "cli:label-resolve-conflict",
			}); err != nil {
				return err
			}
			resolveHardExample(cmd, hardExamples, articleID)
			cmd.Printf("resolved conflict for %s as %s\n", articleID, label)
			return nil
		},
	}
	resolveCmd.Flags().StringVar(&by, "by", "", "who resolved this conflict")
	resolveCmd.Flags().StringVar(&notes, "notes", "", "free-text notes for this resolution")
	return resolveCmd
}
