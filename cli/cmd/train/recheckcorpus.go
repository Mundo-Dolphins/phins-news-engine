package train

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func newRecheckCorpusCommand() *cobra.Command {
	var dictionaryPath string
	cmd := &cobra.Command{
		Use:   "recheck-corpus",
		Short: "Report publish-labeled examples the current dictionary would now classify as not related (read-only)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// root command (and this subcommand's flag vars) are a package-level
			// singleton shared across every CLI invocation within the same
			// process -- Cobra does not reset flag values between Execute()
			// calls. A real `phins-news-engine-tools` invocation is always its
			// own process, so this never matters there, but any caller that
			// invokes this command repeatedly in one process (tests via
			// testsupport.Execute, or an in-process automation script) would
			// otherwise see a prior run's --dictionary silently leak into
			// the next one. Same defensive reset pattern as
			// backfillrejects.go's backfill-rejects command.
			defer func() { dictionaryPath = "" }()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			path := dictionaryPath
			if path == "" {
				path = cfg.Dictionary.Path
			}
			d, err := dictionaryfile.Load(path)
			if err != nil {
				return fmt.Errorf("train recheck-corpus: load dictionary %s: %w", path, err)
			}
			for _, issue := range dictionary.Validate(d) {
				if issue.Severity == dictionary.SeverityError {
					return fmt.Errorf("train recheck-corpus: dictionary %s failed validation: %s", path, issue)
				}
			}
			matcher, err := dictionary.Prepare(d)
			if err != nil {
				return fmt.Errorf("train recheck-corpus: prepare dictionary: %w", err)
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}

			labels := sqliteadapter.NewHumanLabelRepository(db)
			examples, err := labels.ListEditorialExamples(cmd.Context())
			if err != nil {
				return fmt.Errorf("train recheck-corpus: %w", err)
			}

			examined := 0
			flagged := 0
			for _, e := range examples {
				if e.Label != "publish" {
					continue
				}
				examined++
				result := classification.Decide(e.Title, e.Body, matcher)
				if !result.Related {
					flagged++
					cmd.Printf("FLAGGED %s: publish-labeled but dictionary now scores Related=false (score %.2f, rule %s) — title: %q\n",
						e.ArticleID, result.Score, result.RuleApplied, e.Title)
				}
			}
			cmd.Printf("recheck-corpus: %d publish-labeled example(s) examined, %d flagged\n", examined, flagged)
			return nil
		},
	}
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", "", "dictionary file to recheck against (default: dictionary.path from config)")
	return cmd
}
