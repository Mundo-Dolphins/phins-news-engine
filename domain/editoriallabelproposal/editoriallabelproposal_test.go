package editoriallabelproposal_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
)

func TestComputeTier(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	highConfidence := &editoriallabelproposal.Proposal{Label: "publish", Confidence: 0.97, CreatedAt: now}
	lowConfidence := &editoriallabelproposal.Proposal{Label: "publish", Confidence: 0.6, CreatedAt: now}

	tests := []struct {
		name                string
		hasActiveHumanLabel bool
		latestProposal      *editoriallabelproposal.Proposal
		autoLabelThreshold  float64
		want                editoriallabelproposal.Tier
	}{
		{
			name:                "manual label always wins, even with a high-confidence proposal present",
			hasActiveHumanLabel: true,
			latestProposal:      highConfidence,
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierGold,
		},
		{
			name:                "manual label wins with no proposal at all",
			hasActiveHumanLabel: true,
			latestProposal:      nil,
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierGold,
		},
		{
			name:                "high-confidence proposal without a manual label is silver",
			hasActiveHumanLabel: false,
			latestProposal:      highConfidence,
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierSilver,
		},
		{
			name:                "confidence exactly at threshold is silver",
			hasActiveHumanLabel: false,
			latestProposal:      &editoriallabelproposal.Proposal{Confidence: 0.95},
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierSilver,
		},
		{
			name:                "low-confidence proposal without a manual label is pending",
			hasActiveHumanLabel: false,
			latestProposal:      lowConfidence,
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierPending,
		},
		{
			name:                "no proposal and no manual label is pending",
			hasActiveHumanLabel: false,
			latestProposal:      nil,
			autoLabelThreshold:  0.95,
			want:                editoriallabelproposal.TierPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editoriallabelproposal.ComputeTier(tt.hasActiveHumanLabel, tt.latestProposal, tt.autoLabelThreshold)
			if got != tt.want {
				t.Errorf("ComputeTier() = %v, want %v", got, tt.want)
			}
		})
	}
}
