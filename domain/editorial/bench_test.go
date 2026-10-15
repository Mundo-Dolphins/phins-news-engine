package editorial_test

import (
	"fmt"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// benchModel is a synthetic linear model with a few thousand weights, a
// stand-in for a trained artifact (none ships with the library).
func benchModel() editorial.Model {
	weights := make(map[string]float64, 4000)
	for i := 0; i < 4000; i++ {
		weights[fmt.Sprintf("uni:term%d", i)] = float64(i%7-3) / 10
	}
	for _, w := range []string{"isotopes", "sample", "touchdowns", "springfield", "quarterback"} {
		weights["uni:"+w] = 0.8
	}
	return editorial.Model{
		Version: "bench", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		Weights:    weights,
		Thresholds: editorial.Thresholds{Reject: 30, Accept: 70},
	}
}

func BenchmarkEvaluate(b *testing.B) {
	m := benchModel()
	a := article.Article{
		Title:       "Isotopes rally past Sharks as Jordan Sample throws three touchdowns",
		Summary:     "Springfield Isotopes quarterback Jordan Sample threw for 310 yards and three touchdowns Sunday as Springfield defeated Shelbyville 27-24 at Example Stadium.",
		ContentType: article.ContentTypeArticle,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		editorial.Evaluate(a, false, &m)
	}
}
