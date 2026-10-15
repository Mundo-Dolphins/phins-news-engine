package training_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestModelMetadata_Promoted(t *testing.T) {
	promoted := training.ModelMetadata{Version: "v1", Status: "promoted", PromotedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !promoted.Promoted() {
		t.Error("Promoted() = false, want true when Status is \"promoted\"")
	}

	notPromoted := training.ModelMetadata{Version: "v1", Status: "candidate"}
	if notPromoted.Promoted() {
		t.Error("Promoted() = true, want false when Status is \"candidate\"")
	}
}

func TestModelMetadata_Promoted_ChecksStatus(t *testing.T) {
	promoted := training.ModelMetadata{Status: "promoted"}
	if !promoted.Promoted() {
		t.Error("Promoted() = false for Status \"promoted\", want true")
	}

	candidate := training.ModelMetadata{Status: "candidate", PromotedAt: time.Now()}
	if candidate.Promoted() {
		t.Error("Promoted() = true for Status \"candidate\" even with PromotedAt set, want false — Status is authoritative")
	}

	retired := training.ModelMetadata{Status: "retired"}
	if retired.Promoted() {
		t.Error("Promoted() = true for Status \"retired\", want false")
	}
}

func TestModelMetadata_NewFieldsRoundTrip(t *testing.T) {
	m := training.ModelMetadata{
		Version: "v3", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		DatasetHash: "abc123", Thresholds: `{"reject":20,"accept":80}`,
		Config: `{"seed":42}`, ArtifactHash: "def456", Status: "candidate",
	}
	if m.Algorithm != "linear_svm" || m.DatasetHash != "abc123" || m.ArtifactHash != "def456" {
		t.Errorf("fields did not round-trip: %+v", m)
	}
}
