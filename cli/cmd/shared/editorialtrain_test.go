package shared

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func loadRepoConfig(t *testing.T) *config.Config {
	t.Helper()
	root, err := repofs.Root()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root) // dictionary/model paths in settings.yml are repo-root relative
	// settings.yml enables web lookup, whose key is a secret supplied only
	// via the environment; Load rejects the config without it.
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	cfg, err := config.Load("configs/example/settings.yml")
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return cfg
}

func TestEditorialTrainConfig_AppliesModelTrainingSettings(t *testing.T) {
	cfg := loadRepoConfig(t)
	tc, err := EditorialTrainConfig(cfg, "fs-test")
	if err != nil {
		t.Fatalf("EditorialTrainConfig() error = %v", err)
	}
	if tc.AmbiguousPolicy != cfg.Editorial.AmbiguousPolicy {
		t.Errorf("AmbiguousPolicy = %q, want %q", tc.AmbiguousPolicy, cfg.Editorial.AmbiguousPolicy)
	}
	if tc.Thresholds.MaxFalsePositiveRate != cfg.OfflineTraining.ModelTraining.ThresholdMaxFalsePositiveRate {
		t.Errorf("MaxFalsePositiveRate = %v, want %v", tc.Thresholds.MaxFalsePositiveRate, cfg.OfflineTraining.ModelTraining.ThresholdMaxFalsePositiveRate)
	}
	if !tc.Split.StratifyByLabel || !tc.BinaryFeatures || tc.Relevant == nil {
		t.Errorf("TrainConfig = %+v, want stratified split, binary features and a relevance filter from settings", tc)
	}
	if tc.SilverWeight != cfg.OfflineTraining.ModelTraining.SilverWeight || tc.SilverWeight <= 0 {
		t.Errorf("SilverWeight = %v, want settings' silver_weight %v", tc.SilverWeight, cfg.OfflineTraining.ModelTraining.SilverWeight)
	}
	if tc.SchemaVersion != "fs-test" {
		t.Errorf("SchemaVersion = %q, want fs-test", tc.SchemaVersion)
	}
}

func TestEditorialRelevanceFilter_MirrorsProductionRelevanceStage(t *testing.T) {
	cfg := loadRepoConfig(t)
	relevant, err := EditorialRelevanceFilter(cfg)
	if err != nil {
		t.Fatalf("EditorialRelevanceFilter() error = %v", err)
	}
	cases := []struct {
		name string
		ex   editorialtrain.TrainingExample
		want bool
	}{
		{"off-topic chatter", editorialtrain.TrainingExample{SourceID: "bluesky:isotopes.example.com", Title: "First full slate of college baseball on a Saturday. Life is good today."}, false},
		{"team news", editorialtrain.TrainingExample{SourceID: "rss:example-sports-wire", Title: "Springfield Isotopes place Jordan Sample on injured reserve"}, true},
		{"trusted source bypass", editorialtrain.TrainingExample{SourceID: "rss:isotopes-official", Title: "Found a vintage jersey for $72 retail"}, true},
	}
	for _, c := range cases {
		if got := relevant(c.ex); got != c.want {
			t.Errorf("%s: relevant = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEditorialTrainConfig_RelevantOnlyDisabledLeavesFilterNil(t *testing.T) {
	cfg := loadRepoConfig(t)
	cfg.OfflineTraining.ModelTraining.RelevantOnly = false
	tc, err := EditorialTrainConfig(cfg, "fs-test")
	if err != nil {
		t.Fatalf("EditorialTrainConfig() error = %v", err)
	}
	if tc.Relevant != nil {
		t.Error("Relevant != nil, want nil when relevant_only is false")
	}
}
