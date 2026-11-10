package train

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetreview"
)

func displayReviewText(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "… [truncated]"
}

func reviewDatasetExamples(session *datasetreview.Session, input io.Reader, output io.Writer, reviewer string, now time.Time) ([]datasetreview.LogEntry, int, error) {
	candidates := session.Candidates
	byID := make(map[string]int, len(session.Examples))
	for i, ex := range session.Examples {
		byID[ex.ArticleID] = i
	}
	reader := bufio.NewReader(input)
	decisions := make([]datasetreview.LogEntry, 0)
	skipped := 0
	for pos, candidate := range candidates {
		ex := session.Examples[byID[candidate.ArticleID]]
		fmt.Fprintf(output, "\n[%d/%d] %s | %s | current=%s (%s)\n", pos+1, len(candidates), ex.ArticleID, ex.SourceID, ex.Label, ex.LabelSource)
		fmt.Fprintf(output, "audit: %s\ntitle: %s\nbody: %s\n", candidate.Reason, displayReviewText(ex.Title, 1000), displayReviewText(ex.Body, 2000))
		fmt.Fprint(output, "[p] publish / [r] reject / [k] confirm current label / [s] skip / [q] save and quit: ")
		line, readErr := reader.ReadString('\n')
		choice := strings.ToLower(strings.TrimSpace(line))
		switch choice {
		case "q":
			return decisions, skipped + len(candidates) - pos, nil
		case "":
			if readErr == io.EOF {
				return decisions, skipped + len(candidates) - pos, nil
			}
			skipped++
		case "s":
			skipped++
		case "p", "r", "k":
			decision := map[string]datasetreview.Decision{"p": datasetreview.Publish, "r": datasetreview.Reject, "k": datasetreview.Confirm}[choice]
			entry, err := session.Apply(candidate.ArticleID, decision, reviewer, now)
			if err != nil {
				return nil, 0, err
			}
			decisions = append(decisions, entry)
		default:
			skipped++
			fmt.Fprintln(output, "Unrecognized choice; row skipped.")
		}
		if readErr == io.EOF {
			return decisions, skipped + len(candidates) - pos - 1, nil
		}
	}
	return decisions, skipped, nil
}

func newReviewDatasetCommand() *cobra.Command {
	var datasetPath, auditPath, outPath, reviewer string
	cmd := &cobra.Command{
		Use:   "review-dataset",
		Short: "Interactively review a prioritized CSV audit against a dataset and write a reviewed copy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() {
				datasetPath, auditPath, outPath, reviewer = datasetreview.DefaultDataset, datasetreview.DefaultAudit, datasetreview.DefaultOut, ""
			}()
			excludedSources, err := modelBypassSourceSet()
			if err != nil {
				return err
			}
			paths := datasetreview.Paths{Dataset: datasetPath, Audit: auditPath, Out: outPath}
			session, err := datasetreview.Open(paths, excludedSources)
			if errors.Is(err, datasetreview.ErrSamePath) || errors.Is(err, datasetreview.ErrEmptyAudit) {
				return fmt.Errorf("train review-dataset: %w", err)
			}
			if err != nil {
				return err
			}
			cmd.Printf("Reviewing %d prioritized dataset rows. Original dataset stays unchanged.\n", len(session.Candidates))
			decisions, skipped, err := reviewDatasetExamples(session, cmd.InOrStdin(), cmd.OutOrStdout(), reviewer, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := session.Save(time.Now().UTC(), decisions); err != nil {
				return err
			}
			logPath := paths.Log()
			cmd.Printf("reviewed %d decision(s), skipped %d; dataset: %s; decision log: %s\n", len(decisions), skipped, outPath, logPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", datasetreview.DefaultDataset, "source dataset YAML (never modified)")
	cmd.Flags().StringVar(&auditPath, "audit", datasetreview.DefaultAudit, "prioritized audit CSV with article_id and audit_reason columns")
	cmd.Flags().StringVar(&outPath, "out", datasetreview.DefaultOut, "path for the reviewed dataset copy")
	cmd.Flags().StringVar(&reviewer, "by", "", "reviewer name recorded in the decision log")
	_ = cmd.MarkFlagRequired("by")
	return cmd
}
