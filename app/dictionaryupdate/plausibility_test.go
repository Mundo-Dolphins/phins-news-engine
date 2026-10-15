package dictionaryupdate_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

func TestCheckPlausibility(t *testing.T) {
	bounds := dictionaryupdate.PlausibilityBounds{MinimumEntities: 40, MaximumEntities: 130, MaximumMissingRatio: 0.30}

	cases := []struct {
		name     string
		observed int
		previous int
		wantErr  bool
	}{
		{"within bounds, no previous count", 106, 0, false},
		{"below minimum", 0, 0, true},
		{"above maximum", 200, 0, true},
		{"acceptable drop", 90, 106, false},
		{"drop exceeds maximum_missing_ratio", 60, 106, true},
		{"exactly at minimum", 40, 0, false},
		{"exactly at maximum", 130, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := dictionaryupdate.CheckPlausibility(bounds, tc.observed, tc.previous)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckPlausibility(observed=%d, previous=%d) error = %v, wantErr %v", tc.observed, tc.previous, err, tc.wantErr)
			}
		})
	}
}
