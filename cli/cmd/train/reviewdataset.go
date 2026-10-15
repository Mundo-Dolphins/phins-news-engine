package train

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

type auditCandidate struct {
	ArticleID string
	Reason    string
}

type datasetReviewDecision struct {
	ArticleID string    `json:"article_id"`
	Previous  string    `json:"previous_label"`
	Label     string    `json:"reviewed_label"`
	Audit     string    `json:"audit_reason"`
	Reviewer  string    `json:"reviewer"`
	Reviewed  time.Time `json:"reviewed_at"`
}

func readAuditCandidates(r io.Reader) ([]auditCandidate, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read audit CSV header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")] = i
	}
	idCol, hasID := columns["article_id"]
	reasonCol, hasReason := columns["audit_reason"]
	if !hasID || !hasReason {
		return nil, fmt.Errorf("audit CSV must contain article_id and audit_reason columns")
	}
	var out []auditCandidate
	seen := map[string]bool{}
	for line := 2; ; line++ {
		record, readErr := cr.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read audit CSV line %d: %w", line, readErr)
		}
		id := strings.TrimSpace(record[idCol])
		if id == "" {
			return nil, fmt.Errorf("audit CSV line %d has an empty article_id", line)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, auditCandidate{ArticleID: id, Reason: record[reasonCol]})
	}
	return out, nil
}

func displayReviewText(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "… [truncated]"
}

func validateResumeDataset(source, reviewed []editorialtrain.TrainingExample) error {
	if len(source) != len(reviewed) {
		return fmt.Errorf("review output has %d rows but source dataset has %d; use a new --out path", len(reviewed), len(source))
	}
	byID := make(map[string]editorialtrain.TrainingExample, len(source))
	for _, ex := range source {
		byID[ex.ArticleID] = ex
	}
	for _, ex := range reviewed {
		prior, ok := byID[ex.ArticleID]
		if !ok || prior.SourceID != ex.SourceID || prior.ContentHash != ex.ContentHash || prior.Title != ex.Title || prior.Body != ex.Body {
			return fmt.Errorf("review output does not match source snapshot at article %s; use a new --out path", ex.ArticleID)
		}
	}
	return nil
}

func reviewDatasetExamples(examples []editorialtrain.TrainingExample, candidates []auditCandidate, input io.Reader, output io.Writer, reviewer string, now time.Time) ([]datasetReviewDecision, int, error) {
	byID := make(map[string]int, len(examples))
	for i, ex := range examples {
		byID[ex.ArticleID] = i
	}
	for _, c := range candidates {
		if _, ok := byID[c.ArticleID]; !ok {
			return nil, 0, fmt.Errorf("audit article %s is absent from the dataset", c.ArticleID)
		}
	}

	reader := bufio.NewReader(input)
	decisions := make([]datasetReviewDecision, 0)
	skipped := 0
	for pos, candidate := range candidates {
		i := byID[candidate.ArticleID]
		ex := &examples[i]
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
			label := ex.Label
			switch choice {
			case "p":
				label = "publish"
			case "r":
				label = "reject"
			}
			decisions = append(decisions, datasetReviewDecision{ArticleID: ex.ArticleID, Previous: ex.Label, Label: label, Audit: candidate.Reason, Reviewer: reviewer, Reviewed: now})
			ex.Label = label
			ex.LabelSource = "interactive_review"
			ex.Reason = "interactive_dataset_review"
			ex.LabeledAt = now
			ex.Revision++
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

func writeReviewLog(path string, decisions []datasetReviewDecision) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path is an explicit operator-provided CLI output path.
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		if err := w.Write([]string{"article_id", "previous_label", "reviewed_label", "audit_reason", "reviewer", "reviewed_at"}); err != nil {
			return err
		}
	}
	for _, d := range decisions {
		if err := w.Write([]string{d.ArticleID, d.Previous, d.Label, d.Audit, d.Reviewer, d.Reviewed.Format(time.RFC3339Nano)}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func newReviewDatasetCommand() *cobra.Command {
	var datasetPath, auditPath, outPath, reviewer string
	cmd := &cobra.Command{
		Use:   "review-dataset",
		Short: "Interactively review a prioritized CSV audit against a dataset and write a reviewed copy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() {
				datasetPath, auditPath, outPath, reviewer = "data/editorial_dataset.yml", "data/editorial_dataset-review.csv", "data/editorial_dataset-reviewed.yml", ""
			}()
			absIn, err := filepath.Abs(datasetPath)
			if err != nil {
				return err
			}
			absOut, err := filepath.Abs(outPath)
			if err != nil {
				return err
			}
			if absIn == absOut {
				return fmt.Errorf("train review-dataset: --out must differ from --dataset; original dataset is preserved")
			}
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			excludedSources, err := modelBypassSourceSet()
			if err != nil {
				return err
			}
			dataset = dataset.ExcludeSources(excludedSources)
			if _, statErr := os.Stat(outPath); statErr == nil {
				prior, loadErr := editorialtrain.LoadDataset(outPath)
				if loadErr != nil {
					return fmt.Errorf("load existing review output for resuming: %w", loadErr)
				}
				// Older review outputs may still contain official-source rows
				// that are now excluded from training. Drop those rows before
				// checking the saved review snapshot against the filtered source.
				prior = prior.ExcludeSources(excludedSources)
				if resumeErr := validateResumeDataset(dataset.Examples, prior.Examples); resumeErr != nil {
					return resumeErr
				}
				dataset = prior
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			auditFile, err := os.Open(auditPath) // #nosec G304 -- auditPath is an explicit operator-provided CLI input path.
			if err != nil {
				return err
			}
			candidates, readErr := readAuditCandidates(auditFile)
			closeErr := auditFile.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if len(candidates) == 0 {
				return fmt.Errorf("train review-dataset: audit CSV contains no rows")
			}
			cmd.Printf("Reviewing %d prioritized dataset rows. Original dataset stays unchanged.\n", len(candidates))
			decisions, skipped, err := reviewDatasetExamples(dataset.Examples, candidates, cmd.InOrStdin(), cmd.OutOrStdout(), reviewer, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := editorialtrain.SaveDataset(outPath, dataset.Examples); err != nil {
				return err
			}
			reloaded, err := editorialtrain.LoadDataset(outPath)
			if err != nil {
				return err
			}
			if err := editorialtrain.WriteManifest(outPath, dataset.Examples, reloaded.Hash(), time.Now().UTC(), 0, 0); err != nil {
				return err
			}
			logPath := outPath + ".review.csv"
			if err := writeReviewLog(logPath, decisions); err != nil {
				return err
			}
			cmd.Printf("reviewed %d decision(s), skipped %d; dataset: %s; decision log: %s\n", len(decisions), skipped, outPath, logPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "source dataset YAML (never modified)")
	cmd.Flags().StringVar(&auditPath, "audit", "data/editorial_dataset-review.csv", "prioritized audit CSV with article_id and audit_reason columns")
	cmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset-reviewed.yml", "path for the reviewed dataset copy")
	cmd.Flags().StringVar(&reviewer, "by", "", "reviewer name recorded in the decision log")
	_ = cmd.MarkFlagRequired("by")
	return cmd
}
