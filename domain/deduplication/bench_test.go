package deduplication_test

import (
	"fmt"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
)

func BenchmarkFingerprint(b *testing.B) {
	a := article.Article{CanonicalURL: "https://isotopeswire.example.com/story/sports/nfl/isotopes/2026/08/03/headline/"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		deduplication.Fingerprint(a)
	}
}

// BenchmarkDecide_50Candidates exercises Decide at the configured
// max_candidates ceiling (50, see configs/settings.yml
// deduplication.near_duplicate.max_candidates) — the realistic upper bound
// for one dedup decision, never "compare against the whole database."
func BenchmarkDecide_50Candidates(b *testing.B) {
	const n = 50
	candidates := make([]deduplication.Candidate, n)
	scores := make(map[string]float64, n)
	signals := make(map[string][]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("article-%d", i)
		candidates[i] = deduplication.Candidate{ArticleID: id, ClusterID: fmt.Sprintf("cluster-%d", i%5)}
		// Spread scores from 0.30 to 0.93 across the 50 candidates so all
		// three Decide bands are represented (none: <0.55, same_event:
		// [0.55, 0.85), near/duplicate: >=0.85). The resolved threshold for
		// "rss" is 0.85 (ThresholdBySourceType override, not the 0.82
		// config default), and the highest score (0.93) clears it, so the
		// benchmarked call exercises Decide's near-duplicate branch — the
		// production-critical rejection path — on every iteration.
		scores[id] = 0.3 + float64(i%10)*0.07
		signals[id] = []string{"isotopes", "ezo", "touchdown"}
	}
	th := deduplication.ResolveThresholds(defaultThresholdConfig(), "rss", 12) // reuse the fixture helper from decision_test.go; resolves DuplicateThreshold=0.85 (rss override)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		deduplication.Decide(candidates, scores, signals, th)
	}
}
