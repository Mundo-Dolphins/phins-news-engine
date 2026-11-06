// Package datasetaudit holds the dataset and corpus audits shared by the
// `train audit-dataset|audit-gates|recheck-corpus` CLI commands and the admin
// train actions: heuristic review candidates, the publication-gate re-check
// and the dictionary recheck of the publish-labeled corpus.
package datasetaudit

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// Candidate is one dataset row proposed for review.
type Candidate struct {
	// ArticleID identifies the dataset row.
	ArticleID string
	// Reason says why the row was flagged (reasons joined with "; ").
	Reason string
}

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

// AuditDataset lists the rows a reviewer should look at first: titles that
// carry conflicting labels, short bodiless Telegram positives and
// pipeline-backfill rejects.
func AuditDataset(examples []editorialtrain.TrainingExample) []Candidate {
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
	out := make([]Candidate, 0)
	for i, rs := range reasons {
		if len(rs) > 0 {
			out = append(out, Candidate{ArticleID: examples[i].ArticleID, Reason: strings.Join(rs, "; ")})
		}
	}
	return out
}

// GateAuditor holds what the publication gates need to re-judge a stored
// example: the same relevance, anchor and veto rules the online pipeline
// applies today.
type GateAuditor struct {
	// Matcher is the prepared relevance dictionary.
	Matcher dictionary.Matcher
	// Text is the text normalization the classifier uses.
	Text normalization.TextConfig
	// Anchor is the anchor policy applied to related verdicts.
	Anchor classification.AnchorPolicy
	// Trusted lists source IDs whose items skip the gates.
	Trusted map[string]bool
	// Context lists source IDs judged with source context.
	Context map[string]bool
	// Exempt lists model-bypass source IDs, which skip the gates.
	Exempt map[string]bool
	// Veto is the editorial veto rule set (nil: no vetoes).
	Veto *editorialveto.Config
	// Evaluated is the instant the anchor policy is evaluated at.
	Evaluated time.Time
}

// gateReasons returns why the current gates would not let ex through, in
// priority order (empty when they would). Reasons are prefixed so a reviewer
// can filter the CSV: "veto:", "gate:no_anchor", "gate:not_related".
func (g GateAuditor) gateReasons(ex editorialtrain.TrainingExample) []string {
	if g.Exempt[ex.SourceID] || g.Trusted[ex.SourceID] {
		return nil
	}
	var reasons []string
	if g.Veto != nil {
		if v := editorialveto.Evaluate(ex.Title+" "+ex.Body, *g.Veto); v.Triggered {
			reasons = append(reasons, "veto:"+v.ReasonCode)
		}
	}
	title := normalization.Normalize(ex.Title, g.Text).ForClassification
	body := normalization.Normalize(ex.Body, g.Text).ForClassification
	var verdict classification.Result
	if g.Context[ex.SourceID] {
		verdict = classification.DecideWithSourceContext(title, body, g.Matcher)
	} else {
		verdict = classification.Decide(title, body, g.Matcher)
	}
	switch {
	case !verdict.Related:
		reasons = append(reasons, fmt.Sprintf("gate:not_related (rule %s)", verdict.RuleApplied))
	default:
		if anchored := classification.ApplyAnchor(verdict, g.Anchor, g.Evaluated); !anchored.Related {
			reasons = append(reasons, fmt.Sprintf("gate:no_anchor (entities: %s)", strings.Join(verdict.EntitiesDetected, ", ")))
		}
	}
	return reasons
}

// Audit lists the publish-labeled examples the current gates would block,
// vetoes first, then missing anchor, then not related.
func (g GateAuditor) Audit(examples []editorialtrain.TrainingExample) []Candidate {
	var out []Candidate
	for _, ex := range examples {
		if ex.Label != "publish" {
			continue
		}
		if reasons := g.gateReasons(ex); len(reasons) > 0 {
			out = append(out, Candidate{ArticleID: ex.ArticleID, Reason: strings.Join(reasons, "; ")})
		}
	}
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

// WriteCSV writes the candidates as an article_id/audit_reason CSV (parent
// directories created, file mode 0600), the input of `train review-dataset`.
func WriteCSV(path string, cs []Candidate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- explicit operator-provided output path.
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	writeErr := w.Write([]string{"article_id", "audit_reason"})
	for _, c := range cs {
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

// Flagged is a publish-labeled example the current dictionary would now
// classify as not related.
type Flagged struct {
	// ArticleID identifies the example.
	ArticleID string
	// Title is the example's title.
	Title string
	// Rule is the classification rule that decided it.
	Rule string
	// Score is the dictionary score.
	Score float64
}

// RecheckCorpus re-scores the publish-labeled examples with m and returns how
// many were examined and which the dictionary would now reject.
func RecheckCorpus(examples []training.EditorialExample, m dictionary.Matcher) (examined int, flagged []Flagged) {
	for _, e := range examples {
		if e.Label != "publish" {
			continue
		}
		examined++
		if result := classification.Decide(e.Title, e.Body, m); !result.Related {
			flagged = append(flagged, Flagged{ArticleID: e.ArticleID, Title: e.Title, Rule: result.RuleApplied, Score: result.Score})
		}
	}
	return examined, flagged
}
