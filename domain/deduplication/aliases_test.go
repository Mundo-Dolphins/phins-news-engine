package deduplication_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
)

func TestContentFingerprint_IsDeterministicAndCaseSensitiveToInputText(t *testing.T) {
	a := deduplication.ContentFingerprint("isotopes beat sharks")
	b := deduplication.ContentFingerprint("isotopes beat sharks")
	if a != b {
		t.Errorf("ContentFingerprint not deterministic: %q vs %q", a, b)
	}
	c := deduplication.ContentFingerprint("isotopes beat owls")
	if a == c {
		t.Error("ContentFingerprint should differ for different text")
	}
}

func TestAttributeCombo_RoundsPublishedAtToWindow(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 30, 0, time.UTC)
	nearby := time.Date(2026, 7, 16, 12, 1, 45, 0, time.UTC)
	window := 2 * time.Minute

	a := deduplication.AttributeCombo("source-1", "isotopes beat sharks", base, window)
	b := deduplication.AttributeCombo("source-1", "isotopes beat sharks", nearby, window)
	if a != b {
		t.Errorf("AttributeCombo differs within the same rounding window: %q vs %q", a, b)
	}

	far := base.Add(10 * time.Minute)
	c := deduplication.AttributeCombo("source-1", "isotopes beat sharks", far, window)
	if a == c {
		t.Error("AttributeCombo should differ once outside the rounding window")
	}
}

func TestAttributeCombo_DiffersBySourceOrText(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Minute

	a := deduplication.AttributeCombo("source-1", "same text", base, window)
	b := deduplication.AttributeCombo("source-2", "same text", base, window)
	if a == b {
		t.Error("AttributeCombo should differ by source")
	}

	c := deduplication.AttributeCombo("source-1", "different text", base, window)
	if a == c {
		t.Error("AttributeCombo should differ by text")
	}
}

func TestURLAlias_IsCaseAndWhitespaceInsensitive(t *testing.T) {
	a := deduplication.URLAlias("  HTTPS://Example.com/story  ")
	b := deduplication.URLAlias("https://example.com/story")
	if a != b {
		t.Errorf("URLAlias not case/whitespace insensitive: %q vs %q", a, b)
	}
}
