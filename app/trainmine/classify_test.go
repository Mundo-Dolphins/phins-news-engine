package trainmine_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

func TestClassifyHardExample(t *testing.T) {
	tests := []struct {
		name                  string
		nbZone                editorial.Zone
		nbConfidence          float64
		llmLabel              string
		llmConfidence         float64
		minNBConfidence       float64
		llmAutoLabelThreshold float64
		want                  string
	}{
		{
			name:   "hard negative: NB confidently accept, LLM confidently discard",
			nbZone: editorial.ZoneAccept, nbConfidence: 0.92, llmLabel: "discard", llmConfidence: 0.95,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "negative",
		},
		{
			name:   "hard positive: NB confidently reject, LLM confidently publish",
			nbZone: editorial.ZoneReject, nbConfidence: 0.90, llmLabel: "publish", llmConfidence: 0.93,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "positive",
		},
		{
			name:   "agreement: both accept/publish is not a hard example",
			nbZone: editorial.ZoneAccept, nbConfidence: 0.95, llmLabel: "publish", llmConfidence: 0.95,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "",
		},
		{
			name:   "NB confidence below floor",
			nbZone: editorial.ZoneAccept, nbConfidence: 0.70, llmLabel: "discard", llmConfidence: 0.95,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "",
		},
		{
			name:   "LLM confidence below floor",
			nbZone: editorial.ZoneAccept, nbConfidence: 0.95, llmLabel: "discard", llmConfidence: 0.60,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "",
		},
		{
			name:   "NB ambiguous zone never produces a hard example",
			nbZone: editorial.ZoneAmbiguous, nbConfidence: 0.95, llmLabel: "discard", llmConfidence: 0.95,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "",
		},
		{
			name:   "LLM uncertain label never produces a hard example",
			nbZone: editorial.ZoneAccept, nbConfidence: 0.95, llmLabel: "uncertain", llmConfidence: 0.95,
			minNBConfidence: 0.8, llmAutoLabelThreshold: 0.9, want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trainmine.ClassifyHardExample(tt.nbZone, tt.nbConfidence, tt.llmLabel, tt.llmConfidence, tt.minNBConfidence, tt.llmAutoLabelThreshold)
			if got != tt.want {
				t.Errorf("ClassifyHardExample() = %q, want %q", got, tt.want)
			}
		})
	}
}
