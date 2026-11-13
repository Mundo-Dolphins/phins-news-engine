package trainwiring_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

func TestCycleTrainingConfigPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.SQLite.Path = "data/ops/operational.db"
	cfg.Model.Path = "models/editorial/current"
	cfg.Classification.TrustedSourceIDs = []string{"a", "b"}
	cfg.EditorialGates.MinPrecision = 0.8
	excluded := map[string]bool{"x": true}
	got, err := trainwiring.CycleTrainingConfig(cfg, excluded, "fs-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DatasetPath != "data/ops/editorial_cycle_dataset.yml" || got.OutDir != "models/editorial/candidates" ||
		got.PromotedModelPath != cfg.Model.Path || got.TrainCfg.SchemaVersion != "fs-1" || got.GateCfg.MinPrecision != 0.8 {
		t.Errorf("config = %+v", got)
	}
	if !reflect.DeepEqual(got.TrustedSourceIDs, map[string]bool{"a": true, "b": true}) || !reflect.DeepEqual(got.ExcludedSourceIDs, excluded) {
		t.Errorf("sources = %v / %v", got.TrustedSourceIDs, got.ExcludedSourceIDs)
	}
}
