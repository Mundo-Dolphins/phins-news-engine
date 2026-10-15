package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

func nbDataset() []editorialtrain.TrainingExample {
	var examples []editorialtrain.TrainingExample
	for i := 0; i < 8; i++ {
		examples = append(examples, editorialtrain.TrainingExample{ArticleID: "p", Title: "isotopes big win today", Label: "publish"})
	}
	for i := 0; i < 8; i++ {
		examples = append(examples, editorialtrain.TrainingExample{ArticleID: "r", Title: "sponsored shop now deal", Label: "reject"})
	}
	return examples
}

func nbSchema() editorialtrain.FeatureSchema {
	return editorialtrain.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored", "uni:win"}}
}

func TestNaiveBayesTrainer_Algorithm(t *testing.T) {
	tr := editorialtrain.NaiveBayesTrainer{}
	if tr.Algorithm() != "naive_bayes" {
		t.Errorf("Algorithm() = %q, want %q", tr.Algorithm(), "naive_bayes")
	}
}

func TestNaiveBayesTrainer_LearnsCorrectDirection(t *testing.T) {
	tr := editorialtrain.NaiveBayesTrainer{}
	m, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if m.Algorithm != "naive_bayes" {
		t.Errorf("Model.Algorithm = %q, want %q", m.Algorithm, "naive_bayes")
	}
	if m.Weights["uni:isotopes"] <= 0 {
		t.Errorf("weight for uni:isotopes = %v, want > 0 (appears only in publish examples)", m.Weights["uni:isotopes"])
	}
	if m.Weights["uni:sponsored"] >= 0 {
		t.Errorf("weight for uni:sponsored = %v, want < 0 (appears only in reject examples)", m.Weights["uni:sponsored"])
	}

	publishScore := editorial.Predict(m, editorial.FeatureVector{"uni:isotopes": 1})
	rejectScore := editorial.Predict(m, editorial.FeatureVector{"uni:sponsored": 1})
	if publishScore.Value <= rejectScore.Value {
		t.Errorf("publishScore.Value (%v) <= rejectScore.Value (%v), want publish-signal to score higher", publishScore.Value, rejectScore.Value)
	}
}

func TestNaiveBayesTrainer_IgnoresFeaturesOutsideSchema(t *testing.T) {
	tr := editorialtrain.NaiveBayesTrainer{}
	m, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if _, ok := m.Weights["uni:big"]; ok {
		t.Error("Model.Weights contains uni:big, which is not in the schema — Fit must restrict to schema.Features")
	}
}

func TestNaiveBayesTrainer_Deterministic(t *testing.T) {
	tr := editorialtrain.NaiveBayesTrainer{}
	m1, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	m2, err := tr.Fit(nbDataset(), nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if m1.Bias != m2.Bias {
		t.Errorf("Bias differs across identical Fit() calls: %v vs %v", m1.Bias, m2.Bias)
	}
	for f, w := range m1.Weights {
		if m2.Weights[f] != w {
			t.Errorf("weight for %s differs across identical Fit() calls: %v vs %v", f, w, m2.Weights[f])
		}
	}
}

func TestNaiveBayesTrainer_EmptyExamplesReturnsError(t *testing.T) {
	tr := editorialtrain.NaiveBayesTrainer{}
	if _, err := tr.Fit(nil, nbSchema(), 1); err == nil {
		t.Error("Fit(nil examples) error = nil, want an error")
	}
}

func TestNaiveBayesTrainer_BinaryIgnoresRepeatedCounts(t *testing.T) {
	// "isotopes" repeats 5x in one publish example; counted by presence it
	// weighs the same as "sponsored", which appears once per reject.
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "p", Title: "isotopes isotopes isotopes isotopes isotopes", Label: "publish"},
		{ArticleID: "r", Title: "sponsored", Label: "reject"},
	}
	schema := editorialtrain.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}

	m, err := editorialtrain.NaiveBayesTrainer{Binary: true}.Fit(examples, schema, 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	if !m.BinaryFeatures {
		t.Error("Model.BinaryFeatures = false, want true so inference binarizes like training")
	}
	if got, want := m.Weights["uni:isotopes"], -m.Weights["uni:sponsored"]; got != want {
		t.Errorf("weight(isotopes) = %v, want %v (mirror of sponsored under presence counting)", got, want)
	}
}

func TestTrain_BinaryFeaturesProducesBinaryNaiveBayes(t *testing.T) {
	cfg := defaultTrainConfig()
	cfg.Algorithms = []string{"naive_bayes"}
	cfg.BinaryFeatures = true
	candidates, err := editorialtrain.Train(trainDataset(), cfg)
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	if !candidates[0].Model.BinaryFeatures {
		t.Error("naive_bayes candidate BinaryFeatures = false, want true")
	}
}

func silverPublish(n int) []editorialtrain.TrainingExample {
	out := make([]editorialtrain.TrainingExample, n)
	for i := range out {
		out[i] = editorialtrain.TrainingExample{ArticleID: "s", Title: "isotopes big win today", Label: "publish", Tier: "silver"}
	}
	return out
}

func assertModelsEqual(t *testing.T, got, want editorial.Model) {
	t.Helper()
	const eps = 1e-9
	if d := got.Bias - want.Bias; d > eps || d < -eps {
		t.Errorf("Bias = %v, want %v", got.Bias, want.Bias)
	}
	for f, w := range want.Weights {
		if d := got.Weights[f] - w; d > eps || d < -eps {
			t.Errorf("Weights[%q] = %v, want %v", f, got.Weights[f], w)
		}
	}
}

func TestNaiveBayesTrainer_SilverWeightHalvesSilverInfluence(t *testing.T) {
	// 2 Silver publish rows at weight 0.5 must count exactly like 1 Gold
	// publish row: no duplication, just weighted counts.
	weighted := append(nbDataset(), silverPublish(2)...)
	plain := append(nbDataset(), editorialtrain.TrainingExample{ArticleID: "g", Title: "isotopes big win today", Label: "publish"})

	got, err := editorialtrain.NaiveBayesTrainer{SilverWeight: 0.5}.Fit(weighted, nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	want, err := editorialtrain.NaiveBayesTrainer{}.Fit(plain, nbSchema(), 1)
	if err != nil {
		t.Fatalf("Fit() error = %v", err)
	}
	assertModelsEqual(t, got, want)
}

func TestNaiveBayesTrainer_ZeroSilverWeightMeansFullWeight(t *testing.T) {
	data := append(nbDataset(), silverPublish(3)...)
	got, _ := editorialtrain.NaiveBayesTrainer{}.Fit(data, nbSchema(), 1)
	want, _ := editorialtrain.NaiveBayesTrainer{SilverWeight: 1}.Fit(data, nbSchema(), 1)
	assertModelsEqual(t, got, want)
}

func TestNaiveBayesTrainer_EmptyTierIsGold(t *testing.T) {
	data := nbDataset() // all examples have empty Tier
	got, _ := editorialtrain.NaiveBayesTrainer{SilverWeight: 0.3}.Fit(data, nbSchema(), 1)
	want, _ := editorialtrain.NaiveBayesTrainer{}.Fit(data, nbSchema(), 1)
	assertModelsEqual(t, got, want)
}

func TestNaiveBayesTrainer_SilverWeightAppliesAfterBinarizing(t *testing.T) {
	data := append(nbDataset(), silverPublish(2)...)
	plain := append(nbDataset(), editorialtrain.TrainingExample{ArticleID: "g", Title: "isotopes big win today", Label: "publish"})
	got, _ := editorialtrain.NaiveBayesTrainer{Binary: true, SilverWeight: 0.5}.Fit(data, nbSchema(), 1)
	want, _ := editorialtrain.NaiveBayesTrainer{Binary: true}.Fit(plain, nbSchema(), 1)
	assertModelsEqual(t, got, want)
}

func TestNaiveBayesTrainer_RejectsInvalidSilverWeight(t *testing.T) {
	for _, w := range []float64{-0.1, 1.5} {
		if _, err := (editorialtrain.NaiveBayesTrainer{SilverWeight: w}).Fit(nbDataset(), nbSchema(), 1); err == nil {
			t.Errorf("SilverWeight=%v: Fit() error = nil, want error", w)
		}
	}
}
