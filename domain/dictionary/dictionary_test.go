package dictionary_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func TestRegistration_Promoted(t *testing.T) {
	promoted := dictionary.Registration{Version: "v1", PromotedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !promoted.Promoted() {
		t.Error("Promoted() = false, want true when PromotedAt is set")
	}

	notPromoted := dictionary.Registration{Version: "v1"}
	if notPromoted.Promoted() {
		t.Error("Promoted() = true, want false when PromotedAt is zero")
	}
}
