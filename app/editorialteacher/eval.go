package editorialteacher

import (
	"context"
	"hash/fnv"
	"math/rand"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// EvalExample is one human-labeled article used to measure the teacher
// prompt. Truth is the human label, "publish" or "reject".
type EvalExample struct {
	ArticleID  string
	Title      string
	Summary    string
	SourceType string
	Domain     string
	Truth      string
}

// EvalPrediction pairs an example with the teacher's verdict. Label is ""
// when the LLM returned nothing usable for the article.
type EvalPrediction struct {
	EvalExample
	Label      string
	Confidence float64
	Reason     string
}

// EvalReport is the raw outcome of one Evaluate run.
type EvalReport struct {
	Predictions   []EvalPrediction
	BatchesTotal  int
	BatchesFailed int
	// Missing counts examples the LLM returned no label for (failed batch
	// or omitted entry).
	Missing int
}

// Evaluate runs the real teacher prompt (policyText plus the client's
// fixed output instructions) over examples and returns the verdicts. It
// never writes to storage, so a prompt can be iterated on without
// polluting editorial_label_proposals. A failed batch is counted, not
// returned, mirroring Classify.
func Evaluate(ctx context.Context, llm appout.EditorialTeacherLLM, policyText string, examples []EvalExample, batchSize int) (EvalReport, error) {
	if batchSize <= 0 {
		batchSize = 20
	}
	var report EvalReport
	for start := 0; start < len(examples); start += batchSize {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		end := min(start+batchSize, len(examples))
		batch := examples[start:end]
		report.BatchesTotal++

		arts := make([]appout.EditorialTeacherArticle, len(batch))
		for i, ex := range batch {
			arts[i] = appout.EditorialTeacherArticle{
				ArticleID: ex.ArticleID, Title: ex.Title, Summary: ex.Summary,
				SourceType: ex.SourceType, Domain: ex.Domain,
			}
		}
		res, err := llm.Classify(ctx, policyText, arts)
		if err != nil {
			report.BatchesFailed++
			report.Missing += len(batch)
			for _, ex := range batch {
				report.Predictions = append(report.Predictions, EvalPrediction{EvalExample: ex})
			}
			continue
		}
		byID := make(map[string]appout.EditorialTeacherLabel, len(res.Labels))
		for _, l := range res.Labels {
			byID[l.ArticleID] = l
		}
		for _, ex := range batch {
			l, ok := byID[ex.ArticleID]
			if !ok {
				report.Missing++
			}
			report.Predictions = append(report.Predictions, EvalPrediction{
				EvalExample: ex, Label: l.Label, Confidence: l.Confidence, Reason: l.Reason,
			})
		}
	}
	return report, nil
}

// EvalSummary aggregates an EvalReport at one confidence threshold. A
// "discard" is correct when the human label is reject.
type EvalSummary struct {
	Threshold               float64
	DiscardTotal            int
	DiscardCorrect          int
	ConfidentDiscardTotal   int
	ConfidentDiscardCorrect int
	RejectTotal             int
	// ConfidentRejectsCaught counts human rejects the LLM discarded at or
	// above Threshold.
	ConfidentRejectsCaught int
	PublishTotal           int
	// PublishDiscarded counts human publishes the LLM discarded at or
	// above Threshold, the costly error for training.
	PublishDiscarded int
	Uncertain        int
	Missing          int
}

// Summarize counts the report at threshold.
func (r EvalReport) Summarize(threshold float64) EvalSummary {
	s := EvalSummary{Threshold: threshold, Missing: r.Missing}
	for _, p := range r.Predictions {
		switch p.Truth {
		case "reject":
			s.RejectTotal++
		case "publish":
			s.PublishTotal++
		}
		switch p.Label {
		case "uncertain":
			s.Uncertain++
		case "discard":
			s.DiscardTotal++
			correct := p.Truth == "reject"
			if correct {
				s.DiscardCorrect++
			}
			if p.Confidence >= threshold {
				s.ConfidentDiscardTotal++
				if correct {
					s.ConfidentDiscardCorrect++
					s.ConfidentRejectsCaught++
				} else {
					s.PublishDiscarded++
				}
			}
		}
	}
	return s
}

// ConfidentDiscardPrecision is the share of confident discards that were
// human rejects: the quality of the Silver reject labels the prompt would
// produce. Zero when there are none.
func (s EvalSummary) ConfidentDiscardPrecision() float64 {
	if s.ConfidentDiscardTotal == 0 {
		return 0
	}
	return float64(s.ConfidentDiscardCorrect) / float64(s.ConfidentDiscardTotal)
}

// RejectRecall is the share of human rejects caught by a confident discard.
func (s EvalSummary) RejectRecall() float64 {
	if s.RejectTotal == 0 {
		return 0
	}
	return float64(s.ConfidentRejectsCaught) / float64(s.RejectTotal)
}

// SelectEvalExamples returns the examples in the named fold, optionally
// downsampled. fold is "dev" or "holdout" (a stable half chosen by
// hashing the article id, so the two never overlap and never depend on
// ordering) or anything else for all. sample > 0 keeps that many
// examples, drawn with seed. Prompt wording is tuned on dev and confirmed
// on holdout so it is not overfit to the examples that shaped it.
func SelectEvalExamples(examples []EvalExample, fold string, sample int, seed int64) []EvalExample {
	var out []EvalExample
	for _, ex := range examples {
		h := fnv.New32a()
		_, _ = h.Write([]byte(ex.ArticleID))
		inHoldout := h.Sum32()%2 == 1
		switch {
		case fold == "holdout" && !inHoldout, fold == "dev" && inHoldout:
			continue
		}
		out = append(out, ex)
	}
	if sample > 0 && sample < len(out) {
		rng := rand.New(rand.NewSource(seed)) // #nosec G404 -- reproducible sampling, not security
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		out = out[:sample]
	}
	return out
}
