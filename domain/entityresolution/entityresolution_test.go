package entityresolution_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityresolution"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		c       entityresolution.Candidate
		r       entityresolution.Resolution
		minConf float64
		wantErr bool
	}{
		{
			name:    "unresolved is always valid (nothing to check)",
			c:       entityresolution.Candidate{Text: "Random Newcomer", NormalizedText: "random newcomer"},
			r:       entityresolution.Resolution{Resolved: false},
			minConf: 0.7,
			wantErr: false,
		},
		{
			name:    "valid resolution: canonical name contains candidate",
			c:       entityresolution.Candidate{Text: "Newcomer", NormalizedText: "newcomer"},
			r:       entityresolution.Resolution{Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.9},
			minConf: 0.7,
			wantErr: false,
		},
		{
			name:    "valid resolution: shared surname",
			c:       entityresolution.Candidate{Text: "Coach Harrowby", NormalizedText: "coach harrowby"},
			r:       entityresolution.Resolution{Resolved: true, Type: "coach", CanonicalName: "Jeff Harrowby", Confidence: 0.85},
			minConf: 0.7,
			wantErr: false,
		},
		{
			name:    "unknown type rejected",
			c:       entityresolution.Candidate{Text: "Random Newcomer", NormalizedText: "random newcomer"},
			r:       entityresolution.Resolution{Resolved: true, Type: "stadium", CanonicalName: "Random Newcomer", Confidence: 0.9},
			minConf: 0.7,
			wantErr: true,
		},
		{
			name:    "confidence below floor rejected",
			c:       entityresolution.Candidate{Text: "Random Newcomer", NormalizedText: "random newcomer"},
			r:       entityresolution.Resolution{Resolved: true, Type: "player", CanonicalName: "Random Newcomer", Confidence: 0.5},
			minConf: 0.7,
			wantErr: true,
		},
		{
			name:    "empty canonical name rejected",
			c:       entityresolution.Candidate{Text: "Random Newcomer", NormalizedText: "random newcomer"},
			r:       entityresolution.Resolution{Resolved: true, Type: "player", CanonicalName: "", Confidence: 0.9},
			minConf: 0.7,
			wantErr: true,
		},
		{
			name:    "hallucinated unrelated name rejected",
			c:       entityresolution.Candidate{Text: "Random Newcomer", NormalizedText: "random newcomer"},
			r:       entityresolution.Resolution{Resolved: true, Type: "player", CanonicalName: "Tom Bront", Confidence: 0.95},
			minConf: 0.7,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := entityresolution.Validate(tt.c, tt.r, tt.minConf)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
