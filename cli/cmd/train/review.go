package train

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/editoriallabelproposalport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/reviewqueueport"
)

// reviewArticleGetter is the read access runReviewLoop needs over stored
// articles -- satisfied by *sqlite.ArticleRepository.FindByID.
type reviewArticleGetter interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// runReviewLoop drives one interactive review session over entries,
// highest-priority first: for each, it prints the article and its
// NB/LLM verdicts to out, reads one line from in, and acts on the first
// non-whitespace character -- 'p' publish, 'd' discard, 's' skip, 'q'
// quit. Exhausting in (EOF) is treated the same as 'q'. It returns once
// entries is exhausted or the user quits.
func runReviewLoop(
	ctx context.Context,
	out io.Writer,
	in *bufio.Reader,
	entries []reviewpriority.QueueEntry,
	articles reviewArticleGetter,
	labels appout.HumanLabelRepository,
	proposals editoriallabelproposalport.EditorialLabelProposalRepository,
	queue reviewqueueport.ReviewQueueRepository,
	by string,
	now time.Time,
) (published, discarded, skipped int, err error) {
	for _, e := range entries {
		a, err := articles.FindByID(ctx, e.ArticleID)
		if err != nil {
			if errors.Is(err, article.ErrNotFound) {
				// The article was purged (e.g. by retention) after this
				// queue entry was created; drop the orphaned entry and
				// keep going rather than killing the whole interactive
				// session.
				fmt.Fprintf(out, "\n--- %s: article no longer exists (purged); dropping from queue ---\n", e.ArticleID)
				if delErr := queue.Delete(ctx, e.ArticleID); delErr != nil {
					return published, discarded, skipped, fmt.Errorf("train review: dequeue orphaned %s: %w", e.ArticleID, delErr)
				}
				continue
			}
			return published, discarded, skipped, fmt.Errorf("train review: find article %s: %w", e.ArticleID, err)
		}

		fmt.Fprintf(out, "\n--- %s (score %.2f) ---\n", e.ArticleID, e.Score)
		fmt.Fprintf(out, "title:   %s\n", a.Title)
		fmt.Fprintf(out, "summary: %s\n", a.Summary)
		fmt.Fprintf(out, "NB:      zone=%s value=%.1f\n", e.NBZone, e.NBValue)
		fmt.Fprintf(out, "LLM:     label=%s confidence=%.2f\n", e.LLMLabel, e.LLMConfidence)
		fmt.Fprintf(out, "reason:  %s\n", e.Reason)
		fmt.Fprintf(out, "suggested label: %s\n", e.SuggestedLabel)
		fmt.Fprint(out, "[p]ublish / [d]iscard / [s]kip / [q]uit: ")

		line, readErr := in.ReadString('\n')
		if line == "" && readErr != nil {
			// No more input at all (EOF with nothing on this line) --
			// treat as quit, same as an explicit 'q'.
			return published, discarded, skipped, nil
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "p":
			if err := labels.Label(ctx, training.Example{
				ArticleID: e.ArticleID, Label: "publish", LabeledAt: now, LabeledBy: by, Source: "cli:train-review",
			}); err != nil {
				if errors.Is(err, training.ErrAlreadyLabeled) {
					fmt.Fprintf(out, "\n--- %s: already labeled elsewhere; dropping from queue ---\n", e.ArticleID)
					if delErr := queue.Delete(ctx, e.ArticleID); delErr != nil {
						return published, discarded, skipped, fmt.Errorf("train review: dequeue already-labeled %s: %w", e.ArticleID, delErr)
					}
					skipped++
					break
				}
				return published, discarded, skipped, fmt.Errorf("train review: label %s: %w", e.ArticleID, err)
			}
			if err := proposals.UpdateStatus(ctx, e.ProposalID, editoriallabelproposal.StatusApproved, now); err != nil {
				return published, discarded, skipped, fmt.Errorf("train review: approve proposal %s: %w", e.ProposalID, err)
			}
			if err := queue.Delete(ctx, e.ArticleID); err != nil {
				return published, discarded, skipped, fmt.Errorf("train review: dequeue %s: %w", e.ArticleID, err)
			}
			published++
		case "d":
			if err := labels.Label(ctx, training.Example{
				ArticleID: e.ArticleID, Label: "discard", LabeledAt: now, LabeledBy: by, Source: "cli:train-review",
			}); err != nil {
				if errors.Is(err, training.ErrAlreadyLabeled) {
					fmt.Fprintf(out, "\n--- %s: already labeled elsewhere; dropping from queue ---\n", e.ArticleID)
					if delErr := queue.Delete(ctx, e.ArticleID); delErr != nil {
						return published, discarded, skipped, fmt.Errorf("train review: dequeue already-labeled %s: %w", e.ArticleID, delErr)
					}
					skipped++
					break
				}
				return published, discarded, skipped, fmt.Errorf("train review: label %s: %w", e.ArticleID, err)
			}
			if err := proposals.UpdateStatus(ctx, e.ProposalID, editoriallabelproposal.StatusRejected, now); err != nil {
				return published, discarded, skipped, fmt.Errorf("train review: reject proposal %s: %w", e.ProposalID, err)
			}
			if err := queue.Delete(ctx, e.ArticleID); err != nil {
				return published, discarded, skipped, fmt.Errorf("train review: dequeue %s: %w", e.ArticleID, err)
			}
			discarded++
		case "q":
			return published, discarded, skipped, nil
		default:
			skipped++
		}

		if readErr != nil {
			// The line we just processed was the last one available.
			return published, discarded, skipped, nil
		}
	}
	return published, discarded, skipped, nil
}

func newReviewCommand() *cobra.Command {
	var by string
	var limit int
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Interactively review the LLM teacher's queued proposals and record Gold decisions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}

			queueRepo := sqlitereviewqueue.NewReviewQueueRepository(db)
			entries, err := queueRepo.ListOrderedByScore(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				cmd.Println("review queue is empty")
				return nil
			}

			articlesRepo := sqliteadapter.NewArticleRepository(db)
			labelsRepo := sqliteadapter.NewHumanLabelRepository(db)
			proposalsRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)

			published, discarded, skipped, err := runReviewLoop(
				cmd.Context(), cmd.OutOrStdout(), bufio.NewReader(cmd.InOrStdin()),
				entries, articlesRepo, labelsRepo, proposalsRepo, queueRepo, by, time.Now().UTC(),
			)
			cmd.Printf("\npublished: %d, discarded: %d, skipped: %d\n", published, discarded, skipped)
			return err
		},
	}
	cmd.Flags().StringVar(&by, "by", "", "who is performing this review (recorded as labeled_by)")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum queued examples to review in this session")
	return cmd
}
