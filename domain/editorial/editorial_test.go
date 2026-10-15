package editorial_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

func TestPredict_LinearCombinationAndMonotonicSquash(t *testing.T) {
	m := editorial.Model{
		Version: "v1", Bias: 0,
		Weights:    map[string]float64{"a": 2, "b": -1},
		Thresholds: editorial.Thresholds{Reject: 20, Accept: 80},
	}
	fv := editorial.FeatureVector{"a": 1, "b": 1} // raw = 0 + 2*1 + -1*1 = 1
	got := editorial.Predict(m, fv)

	wantValue := 75.0
	if math.Abs(got.Value-wantValue) > 1e-9 {
		t.Errorf("Value = %v, want %v", got.Value, wantValue)
	}
	if got.Raw != 1 {
		t.Errorf("Raw = %v, want 1", got.Raw)
	}
	if got.ModelVersion != "v1" {
		t.Errorf("ModelVersion = %q, want %q", got.ModelVersion, "v1")
	}
	if got.Placeholder {
		t.Error("Placeholder = true, want false for a real Predict call")
	}
}

func TestPredict_FallbackThresholdsKeepFiniteScoresAmbiguous(t *testing.T) {
	m := editorial.Model{
		Bias: 1e6, Weights: map[string]float64{},
		Thresholds: editorial.Thresholds{Reject: 0, Accept: 100},
	}
	got := editorial.Predict(m, editorial.FeatureVector{})
	if got.Value >= 100 || got.Zone != editorial.ZoneAmbiguous {
		t.Errorf("Predict() = value %.17g zone %q, want value < 100 and ambiguous", got.Value, got.Zone)
	}
	if math.IsNaN(got.Value) || math.IsInf(got.Value, 0) {
		t.Errorf("Predict() Value = %v, want finite score", got.Value)
	}
}

func TestPredict_UnknownFeaturesAreIgnored(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{"known": 5}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
	fv := editorial.FeatureVector{"known": 1, "totally_unseen_feature": 999}
	got := editorial.Predict(m, fv)
	want := editorial.Predict(m, editorial.FeatureVector{"known": 1})
	if got.Value != want.Value {
		t.Errorf("unknown feature changed Value: got %v, want %v (same as without it)", got.Value, want.Value)
	}
}

func TestPredict_MissingModelFeatureTreatedAsZero(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{"present": 3, "absent_from_input": 100}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
	got := editorial.Predict(m, editorial.FeatureVector{"present": 2}) // raw = 3*2 + 100*0 = 6
	if got.Raw != 6 {
		t.Errorf("Raw = %v, want 6 (absent feature contributes 0)", got.Raw)
	}
}

func TestPredict_Zones(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{"x": 1}, Thresholds: editorial.Thresholds{Reject: 30, Accept: 70}}
	tests := []struct {
		name string
		x    float64
		want editorial.Zone
	}{
		{"deep negative -> reject", -10, editorial.ZoneReject},
		{"deep positive -> accept", 10, editorial.ZoneAccept},
		{"raw 0 (Value 50) -> ambiguous", 0, editorial.ZoneAmbiguous},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editorial.Predict(m, editorial.FeatureVector{"x": tt.x})
			if got.Zone != tt.want {
				t.Errorf("Zone = %q (value %.2f), want %q", got.Zone, got.Value, tt.want)
			}
		})
	}
}

func TestPredict_ZoneBoundariesAreInclusive(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 50, Accept: 50}}
	// raw always 0 -> Value always exactly 50, equal to both boundaries.
	got := editorial.Predict(m, editorial.FeatureVector{})
	if got.Zone != editorial.ZoneAccept {
		t.Errorf("Zone = %q, want %q (Accept check runs after Reject and both are inclusive)", got.Zone, editorial.ZoneAccept)
	}
}

func TestEvaluate_PlaceholderReturnsNeutralScore(t *testing.T) {
	got := editorial.Evaluate(article.Article{Title: "anything"}, false, nil)

	if got.Value != editorial.NeutralScore {
		t.Errorf("Value = %v, want %v", got.Value, editorial.NeutralScore)
	}
	if !got.Placeholder {
		t.Errorf("Placeholder = false, want true")
	}
}

func TestExtractFeatures_EmptyBodyMatchesPreExistingBehaviorExactly(t *testing.T) {
	a := article.Article{Title: "Isotopes win big game", Summary: "A great win for Springfield."}
	fv := editorial.ExtractFeatures(a, false)
	if _, ok := fv["len:body_full_tokens"]; ok {
		t.Error(`fv has "len:body_full_tokens" key, want none when Body is empty`)
	}
	for k := range fv {
		if strings.HasPrefix(k, "bodytok:") || strings.HasPrefix(k, "bodybi:") {
			t.Errorf("unexpected body-derived key %q with empty Body", k)
		}
	}
}

func TestExtractFeatures_BodyAddsNamespacedFeaturesWithoutChangingExisting(t *testing.T) {
	noBody := article.Article{Title: "Isotopes win big game", Summary: "A great win for Springfield."}
	withBody := noBody
	withBody.Body = "The Springfield Isotopes won a great game today against their rivals in overtime, capping a dramatic comeback."

	fvNoBody := editorial.ExtractFeatures(noBody, false)
	fvWithBody := editorial.ExtractFeatures(withBody, false)

	for k, v := range fvNoBody {
		got, ok := fvWithBody[k]
		if !ok || got != v {
			t.Errorf("pre-existing feature %q changed: got (%v, present=%v), want %v", k, got, ok, v)
		}
	}
	if fvWithBody["bodytok:isotopes"] <= 0 {
		t.Errorf(`fvWithBody["bodytok:isotopes"] = %v, want > 0`, fvWithBody["bodytok:isotopes"])
	}
	if fvWithBody["bodybi:springfield_isotopes"] <= 0 {
		t.Errorf(`fvWithBody["bodybi:springfield_isotopes"] = %v, want > 0`, fvWithBody["bodybi:springfield_isotopes"])
	}
	if fvWithBody["len:body_full_tokens"] <= 0 {
		t.Error("len:body_full_tokens not set for non-empty Body")
	}
}

func TestPredict_BinaryFeaturesScoresPresenceNotCount(t *testing.T) {
	m := editorial.Model{Bias: 0, Weights: map[string]float64{"a": 2, "b": -1}, BinaryFeatures: true}
	got := editorial.Predict(m, editorial.FeatureVector{"a": 5, "b": 0.25, "c": 9})
	if got.Raw != 1 { // 2*1 + -1*1
		t.Errorf("Raw = %v, want 1 (each present feature counted once)", got.Raw)
	}
	zero := editorial.Predict(m, editorial.FeatureVector{"a": 0})
	if zero.Raw != 0 {
		t.Errorf("Raw = %v, want 0 (a zero-valued feature is absent)", zero.Raw)
	}
}
