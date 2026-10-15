package editorialtrain

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
)

const maxSampleIDs = 20 // caps FalsePositives/FalseNegatives so a large dataset doesn't blow up the report

// BinaryMetrics is a confusion-matrix-derived precision/recall/F1 view —
// same shape as application/dedupeval.BinaryMetrics, duplicated rather
// than imported since the two packages address unrelated evaluation
// domains and neither should depend on the other.
type BinaryMetrics struct {
	TP, FP, FN, TN int
}

func (m BinaryMetrics) Precision() float64 {
	if m.TP+m.FP == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FP)
}

func (m BinaryMetrics) Recall() float64 {
	if m.TP+m.FN == 0 {
		return 0
	}
	return float64(m.TP) / float64(m.TP+m.FN)
}

func (m BinaryMetrics) F1() float64 {
	p, r := m.Precision(), m.Recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// FalsePositiveRate is FP / (FP + TN): the share of true discards that
// would have been published.
func (m BinaryMetrics) FalsePositiveRate() float64 {
	if m.FP+m.TN == 0 {
		return 0
	}
	return float64(m.FP) / float64(m.FP+m.TN)
}

// FalseNegativeRate is FN / (FN + TP): the share of true publishes that
// would have been missed.
func (m BinaryMetrics) FalseNegativeRate() float64 {
	if m.FN+m.TP == 0 {
		return 0
	}
	return float64(m.FN) / float64(m.FN+m.TP)
}

func (m *BinaryMetrics) record(predictedPublish, goldPublish bool) {
	switch {
	case predictedPublish && goldPublish:
		m.TP++
	case predictedPublish && !goldPublish:
		m.FP++
	case !predictedPublish && goldPublish:
		m.FN++
	default:
		m.TN++
	}
}

// CalibrationBucket is one decile of a reliability diagram: among examples
// whose Score.Value fell in [RangeLow,RangeHigh), what fraction were
// actually gold-labeled "publish"? A well-calibrated model has
// ActualPublishRate close to the bucket's midpoint.
type CalibrationBucket struct {
	RangeLow, RangeHigh float64
	Count               int
	ActualPublishRate   float64
}

// VetoImpact isolates how much of a deterministic veto layer's would-be
// effect the trained model's own score does NOT already cover — computed
// only when Evaluate is given a non-nil vetoCfg. See the design doc §3.
type VetoImpact struct {
	Triggered                 int // veto fired at all
	TriggeredModelWouldAccept int // veto fired, model's own zone was not reject — what the model alone would have missed
	TriggeredModelWouldReject int // veto fired, model's own zone was also reject — redundant with the model
	NotTriggered              int
}

// Report is the full output of Evaluate.
type Report struct {
	Overall BinaryMetrics
	PRAUC   float64
	// ManualReviewRate is the share of examples this model routed to
	// editorial.ZoneAmbiguous — the project's Part 18 "how much manual
	// intervention would be required" metric. Tracked separately from
	// Confusion so it reads as a first-class number, not something a
	// caller has to derive by summing the ambiguous row every time.
	ManualReviewRate  float64
	Confusion         map[string]map[string]int // predicted zone -> gold label -> count
	FalsePositives    []string                  // article IDs, capped at maxSampleIDs
	FalseNegatives    []string
	BySource          map[string]BinaryMetrics
	ByContentType     map[string]BinaryMetrics
	ByPeriod          map[string]BinaryMetrics // keyed by "2026-01" (PublishedAt month)
	Calibration       []CalibrationBucket
	ModelSizeBytes    int
	InferenceNanos    int64
	MemoryBytesApprox int
	VetoImpact        *VetoImpact
}

// scoredExample pairs a TrainingExample with its editorial.Score — the
// shared unit prAUC/calibration/Evaluate all sweep over.
type scoredExample struct {
	example TrainingExample
	score   editorial.Score
}

var timeNow = time.Now

// Evaluate scores examples against m using the configured enforce-mode
// ambiguous policy. The optional policy defaults to "accept" for backwards
// compatibility with training callers that predate the explicit policy.
func Evaluate(m editorial.Model, examples []TrainingExample, vetoCfg *editorialveto.Config, ambiguousPolicy ...string) Report {
	policy := "accept"
	if len(ambiguousPolicy) > 0 && ambiguousPolicy[0] == "reject" {
		policy = "reject"
	}
	report := Report{
		Confusion:     map[string]map[string]int{},
		BySource:      map[string]BinaryMetrics{},
		ByContentType: map[string]BinaryMetrics{},
		ByPeriod:      map[string]BinaryMetrics{},
	}

	scored := make([]scoredExample, len(examples))
	for i, ex := range examples {
		scored[i] = scoredExample{ex, editorial.Predict(m, ex.Features())}
	}

	ambiguousCount := 0
	for _, s := range scored {
		goldPublish := s.example.Label == "publish"
		predictedPublish := s.score.Zone == editorial.ZoneAccept || (s.score.Zone == editorial.ZoneAmbiguous && policy == "accept")

		report.Overall.record(predictedPublish, goldPublish)
		recordBy(report.BySource, s.example.SourceID, predictedPublish, goldPublish)
		recordBy(report.ByContentType, s.example.ContentType, predictedPublish, goldPublish)
		recordBy(report.ByPeriod, periodKey(s.example.PublishedAt), predictedPublish, goldPublish)

		if s.score.Zone == editorial.ZoneAmbiguous {
			ambiguousCount++
		}

		goldLabel := "reject"
		if goldPublish {
			goldLabel = "publish"
		}
		if report.Confusion[string(s.score.Zone)] == nil {
			report.Confusion[string(s.score.Zone)] = map[string]int{}
		}
		report.Confusion[string(s.score.Zone)][goldLabel]++

		if predictedPublish && !goldPublish && len(report.FalsePositives) < maxSampleIDs {
			report.FalsePositives = append(report.FalsePositives, s.example.ArticleID)
		}
		if !predictedPublish && goldPublish && len(report.FalseNegatives) < maxSampleIDs {
			report.FalseNegatives = append(report.FalseNegatives, s.example.ArticleID)
		}
	}

	if len(scored) > 0 {
		report.ManualReviewRate = float64(ambiguousCount) / float64(len(scored))
	}

	if vetoCfg != nil {
		impact := &VetoImpact{}
		for _, s := range scored {
			v := editorialveto.Evaluate(s.example.Title+" "+s.example.Body, *vetoCfg)
			switch {
			case !v.Triggered:
				impact.NotTriggered++
			case s.score.Zone == editorial.ZoneReject || (s.score.Zone == editorial.ZoneAmbiguous && policy == "reject"):
				impact.Triggered++
				impact.TriggeredModelWouldReject++
			default:
				impact.Triggered++
				impact.TriggeredModelWouldAccept++
			}
		}
		report.VetoImpact = impact
	}

	report.PRAUC = prAUC(scored)
	report.Calibration = calibration(scored)
	report.ModelSizeBytes = modelSizeBytes(m)
	report.InferenceNanos = inferenceNanos(m, examples)
	report.MemoryBytesApprox = len(m.Weights) * 64 // rough per-entry estimate: key bytes + float64 + map overhead

	return report
}

func recordBy[K comparable](m map[K]BinaryMetrics, key K, predictedPublish, goldPublish bool) {
	metrics := m[key]
	metrics.record(predictedPublish, goldPublish)
	m[key] = metrics
}

func periodKey(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01")
}

// prAUC computes an approximate area under the precision-recall curve by
// sweeping examples in descending score order and integrating the
// resulting (recall,precision) points with the trapezoidal rule. This is
// a simplified estimator (no interpolation across ties) appropriate for
// this package's small offline datasets — not a numerically exact
// average-precision implementation.
func prAUC(scoredExamples []scoredExample) float64 {
	type se struct {
		goldPublish bool
		value       float64
	}
	items := make([]se, len(scoredExamples))
	totalPositives := 0
	for i, s := range scoredExamples {
		goldPublish := s.example.Label == "publish"
		items[i] = se{goldPublish, s.score.Value}
		if goldPublish {
			totalPositives++
		}
	}
	if totalPositives == 0 {
		return 0
	}
	sort.Slice(items, func(i, j int) bool { return items[i].value > items[j].value })

	tp, fp := 0, 0
	prevRecall, prevPrecision := 0.0, 1.0
	area := 0.0
	for _, it := range items {
		if it.goldPublish {
			tp++
		} else {
			fp++
		}
		precision := float64(tp) / float64(tp+fp)
		recall := float64(tp) / float64(totalPositives)
		area += (recall - prevRecall) * (precision + prevPrecision) / 2
		prevRecall, prevPrecision = recall, precision
	}
	return area
}

func calibration(scoredExamples []scoredExample) []CalibrationBucket {
	const bucketWidth = 10.0
	buckets := make([]CalibrationBucket, 10)
	published := make([]int, 10)
	for i := range buckets {
		buckets[i] = CalibrationBucket{RangeLow: float64(i) * bucketWidth, RangeHigh: float64(i+1) * bucketWidth}
	}
	for _, s := range scoredExamples {
		idx := int(s.score.Value / bucketWidth)
		if idx < 0 {
			idx = 0
		}
		if idx > 9 {
			idx = 9
		}
		buckets[idx].Count++
		if s.example.Label == "publish" {
			published[idx]++
		}
	}
	var out []CalibrationBucket
	for i, b := range buckets {
		if b.Count == 0 {
			continue
		}
		b.ActualPublishRate = float64(published[i]) / float64(b.Count)
		out = append(out, b)
	}
	return out
}

func modelSizeBytes(m editorial.Model) int {
	data, err := json.Marshal(m.Weights)
	if err != nil {
		return 0
	}
	return len(data) + 64 // + a small constant for bias/version/thresholds overhead
}

func inferenceNanos(m editorial.Model, examples []TrainingExample) int64 {
	if len(examples) == 0 {
		return 0
	}
	fv := examples[0].Features()
	const iterations = 1000
	start := timeNow()
	for i := 0; i < iterations; i++ {
		editorial.Predict(m, fv)
	}
	elapsed := timeNow().Sub(start)
	return elapsed.Nanoseconds() / iterations
}
