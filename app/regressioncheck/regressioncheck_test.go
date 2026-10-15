package regressioncheck_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification/regressiondata"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func TestRun_NoFlipsForIdenticalDictionaries(t *testing.T) {
	d := dictionary.Dictionary{
		Decision: dictionary.Decision{PositiveThresholdHigh: 1, PositiveThresholdMedium: 0.5, MaxCountPerTerm: 3, TitleWeightMultiplier: 2},
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 5, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Confidence: 1}}},
		},
	}

	report, err := regressioncheck.Run(d, d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.FlipCount != 0 {
		t.Fatalf("expected 0 flips comparing a dictionary against itself, got %d: %+v", report.FlipCount, report.Flips)
	}
	if report.TotalCases == 0 {
		t.Fatal("expected the embedded regression dataset to be non-empty")
	}
}

func TestRun_DetectsAFlipWhenAnEntityIsRemoved(t *testing.T) {
	withEntity := dictionary.Dictionary{
		Decision: dictionary.Decision{PositiveThresholdHigh: 1, PositiveThresholdMedium: 0.5, MaxCountPerTerm: 3, TitleWeightMultiplier: 2},
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 5, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Confidence: 1}}},
		},
	}
	withoutEntity := dictionary.Dictionary{
		Decision:   withEntity.Decision,
		Categories: []dictionary.Category{{Name: "official_names", Weight: 5, StrongOverride: true}},
	}

	report, err := regressioncheck.Run(withEntity, withoutEntity)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.FlipCount == 0 {
		t.Fatal("expected at least one flip when the strong-override team name entity is removed")
	}
}

func TestRun_FailsLoudlyWhenCasesDoNotFitTheDictionary(t *testing.T) {
	other := dictionary.Dictionary{
		Decision: dictionary.Decision{PositiveThresholdHigh: 1, PositiveThresholdMedium: 0.5, MaxCountPerTerm: 3, TitleWeightMultiplier: 2},
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 5, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Glimmerfish Rovers", Confidence: 1}}},
		},
	}
	_, err := regressioncheck.Run(other, other)
	if !errors.Is(err, regressioncheck.ErrCasesDoNotFitDictionary) {
		t.Fatalf("Run() error = %v, want ErrCasesDoNotFitDictionary", err)
	}
	if !strings.Contains(err.Error(), "regressiondata.SetCases") {
		t.Errorf("error %q must tell the host to call regressiondata.SetCases", err)
	}

	t.Cleanup(func() { regressiondata.SetCases(nil) })
	regressiondata.SetCases([]regressiondata.Case{{Name: "host case", Title: "Glimmerfish Rovers win", WantRelated: true}})
	if _, err := regressioncheck.Run(other, other); err != nil {
		t.Fatalf("Run() with fitting host cases error = %v, want nil", err)
	}
}
