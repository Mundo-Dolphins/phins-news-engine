package train

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
)

// gateAudit holds what the publication gates need to re-judge a stored
// example: the same relevance, anchor and veto rules the online pipeline
// applies today.
type gateAudit struct {
	matcher   dictionary.Matcher
	text      normalization.TextConfig
	cls       classification.AnchorPolicy
	trusted   map[string]bool
	context   map[string]bool
	exempt    map[string]bool
	veto      *editorialveto.Config
	evaluated time.Time
}

// gateReasons returns why the current gates would not let ex through, in
// priority order (empty when they would). Reasons are prefixed so a reviewer
// can filter the CSV: "veto:", "gate:no_anchor", "gate:not_related".
func (g gateAudit) gateReasons(ex editorialtrain.TrainingExample) []string {
	if g.exempt[ex.SourceID] || g.trusted[ex.SourceID] {
		return nil
	}
	var reasons []string
	if g.veto != nil {
		if v := editorialveto.Evaluate(ex.Title+" "+ex.Body, *g.veto); v.Triggered {
			reasons = append(reasons, "veto:"+v.ReasonCode)
		}
	}
	title := normalization.Normalize(ex.Title, g.text).ForClassification
	body := normalization.Normalize(ex.Body, g.text).ForClassification
	var verdict classification.Result
	if g.context[ex.SourceID] {
		verdict = classification.DecideWithSourceContext(title, body, g.matcher)
	} else {
		verdict = classification.Decide(title, body, g.matcher)
	}
	switch {
	case !verdict.Related:
		reasons = append(reasons, fmt.Sprintf("gate:not_related (rule %s)", verdict.RuleApplied))
	default:
		if anchored := classification.ApplyAnchor(verdict, g.cls, g.evaluated); !anchored.Related {
			reasons = append(reasons, fmt.Sprintf("gate:no_anchor (entities: %s)", strings.Join(verdict.EntitiesDetected, ", ")))
		}
	}
	return reasons
}

// auditGates lists the publish-labeled examples the current gates would block.
func (g gateAudit) auditGates(examples []editorialtrain.TrainingExample) []auditCandidate {
	var out []auditCandidate
	for _, ex := range examples {
		if ex.Label != "publish" {
			continue
		}
		if reasons := g.gateReasons(ex); len(reasons) > 0 {
			out = append(out, auditCandidate{ArticleID: ex.ArticleID, Reason: strings.Join(reasons, "; ")})
		}
	}
	// Vetoes first (the most certain), then missing anchor, then not related.
	rank := func(reason string) int {
		switch {
		case strings.HasPrefix(reason, "veto:"):
			return 0
		case strings.Contains(reason, "gate:no_anchor"):
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Reason) < rank(out[j].Reason) })
	return out
}

func newAuditGatesCommand() *cobra.Command {
	var datasetPath, outPath string
	var limit int
	cmd := &cobra.Command{
		Use:   "audit-gates",
		Short: "List publish-labeled dataset rows the current relevance, anchor and veto gates would block (review queue)",
		Long: `Re-runs every publish-labeled example of a dataset through the gates the
online pipeline applies today (veto rules, dictionary relevance, anchor policy)
and writes the ones that would now be blocked as a review queue. The positives
imported from the channel are whatever the bot published, noise included; this
finds that noise so it can be relabeled as reject with "train review-dataset"
before retraining. Read-only: it never changes labels.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() {
				datasetPath, outPath, limit = "data/editorial_dataset.yml", "data/editorial_dataset-gates-review.csv", 0
			}()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			d, err := dictionaryfile.Load(cfg.Dictionary.Path)
			if err != nil {
				return fmt.Errorf("train audit-gates: load dictionary: %w", err)
			}
			matcher, err := dictionary.Prepare(d)
			if err != nil {
				return fmt.Errorf("train audit-gates: prepare dictionary: %w", err)
			}
			cascade := cfg.DedupCascade()
			g := gateAudit{
				matcher: matcher, text: cascade.Normalization.Text, cls: cascade.Classification.Anchor,
				trusted: cascade.Classification.TrustedSourceIDs, context: cascade.Classification.ContextSourceIDs,
				exempt: cascade.Editorial.ModelBypassSourceIDs, evaluated: time.Now(),
			}
			if cfg.Editorial.VetoPath != "" {
				v, err := infraeditorialveto.Load(cfg.Editorial.VetoPath)
				if err != nil {
					return fmt.Errorf("train audit-gates: load vetoes: %w", err)
				}
				g.veto = &v
			}
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			candidates := g.auditGates(dataset.Examples)
			total := len(candidates)
			if limit > 0 && len(candidates) > limit {
				candidates = candidates[:limit]
			}
			if err := writeAuditCSV(outPath, candidates); err != nil {
				return fmt.Errorf("train audit-gates: %w", err)
			}
			cmd.Printf("audited %d row(s); %d publish-labeled row(s) would now be blocked, %d written to %s\n", len(dataset.Examples), total, len(candidates), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "dataset YAML to audit")
	cmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset-gates-review.csv", "CSV review queue to write (input of train review-dataset --audit)")
	cmd.Flags().IntVar(&limit, "limit", 0, "write at most this many rows, most certain first (0: all)")
	return cmd
}

func writeAuditCSV(path string, candidates []auditCandidate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- explicit operator-provided CLI output path.
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
	return writeErr
}
