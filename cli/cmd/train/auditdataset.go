package train

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

var auditURLPattern = regexp.MustCompile(`https?://\S+`)

func normalizedAuditTitle(title string) string {
	title = strings.ToLower(norm.NFKC.String(auditURLPattern.ReplaceAllString(title, " ")))
	var b strings.Builder
	space := true
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func buildDatasetAudit(examples []editorialtrain.TrainingExample) []auditCandidate {
	byTitle := make(map[string][]int)
	for i, ex := range examples {
		if key := normalizedAuditTitle(ex.Title); key != "" {
			byTitle[key] = append(byTitle[key], i)
		}
	}
	reasons := make([][]string, len(examples))
	for _, indexes := range byTitle {
		labels := map[string]bool{}
		for _, i := range indexes {
			labels[examples[i].Label] = true
		}
		if len(labels) > 1 {
			for _, i := range indexes {
				reasons[i] = append(reasons[i], "same normalized title has conflicting labels")
			}
		}
	}
	for i, ex := range examples {
		if ex.Label == "publish" && ex.LabelSource == "telegramimport" && strings.TrimSpace(ex.Body) == "" && len([]rune(strings.TrimSpace(ex.Title))) <= 50 {
			reasons[i] = append(reasons[i], "Telegram positive with empty body and short title (<=50 chars)")
		}
		if ex.Label == "reject" && ex.LabelSource == "pipeline_backfill" {
			reasons[i] = append(reasons[i], "pipeline_backfill reject; verify direct editorial quality")
		}
	}
	out := make([]auditCandidate, 0)
	for i, rs := range reasons {
		if len(rs) > 0 {
			out = append(out, auditCandidate{ArticleID: examples[i].ArticleID, Reason: strings.Join(rs, "; ")})
		}
	}
	return out
}

func newAuditDatasetCommand() *cobra.Command {
	var datasetPath, outPath string
	cmd := &cobra.Command{
		Use:   "audit-dataset",
		Short: "Generate a prioritized CSV of heuristic dataset rows for interactive review",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { datasetPath, outPath = "data/editorial_dataset.yml", "data/editorial_dataset-review.csv" }()
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			originalCount := len(dataset.Examples)
			excludedSources, err := modelBypassSourceSet()
			if err != nil {
				return err
			}
			dataset = dataset.ExcludeSources(excludedSources)
			candidates := buildDatasetAudit(dataset.Examples)
			if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- outPath is an explicit operator-provided CLI output path.
			if err != nil {
				return err
			}
			w := csv.NewWriter(f)
			writeErr := w.Write([]string{"article_id", "audit_reason"})
			for _, c := range candidates {
				if writeErr != nil {
					break
				}
				writeErr = w.Write([]string{c.ArticleID, c.Reason})
			}
			w.Flush()
			if writeErr == nil {
				writeErr = w.Error()
			}
			if closeErr := f.Close(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				return fmt.Errorf("train audit-dataset: write %s: %w", outPath, writeErr)
			}
			cmd.Printf("audited %d eligible dataset row(s), excluding %d official-source row(s); %d candidate(s) written to %s\n", len(dataset.Examples), originalCount-len(dataset.Examples), len(candidates), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "dataset YAML to audit")
	cmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset-review.csv", "CSV path to write prioritized candidates")
	return cmd
}
