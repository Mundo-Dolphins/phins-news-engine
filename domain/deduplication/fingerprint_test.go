package deduplication_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
)

func TestFingerprint_SameCanonicalURLProducesSameFingerprint(t *testing.T) {
	a := article.Article{CanonicalURL: "https://example.com/story"}
	b := article.Article{CanonicalURL: "https://example.com/story"}

	if deduplication.Fingerprint(a) != deduplication.Fingerprint(b) {
		t.Errorf("expected same fingerprint for identical canonical URLs")
	}
}

func TestFingerprint_CaseAndWhitespaceInsensitive(t *testing.T) {
	a := article.Article{CanonicalURL: "https://example.com/story"}
	b := article.Article{CanonicalURL: "  HTTPS://EXAMPLE.COM/story  "}

	if deduplication.Fingerprint(a) != deduplication.Fingerprint(b) {
		t.Errorf("expected fingerprint to be case and whitespace insensitive")
	}
}

func TestFingerprint_DifferentURLsProduceDifferentFingerprints(t *testing.T) {
	a := article.Article{CanonicalURL: "https://example.com/story-a"}
	b := article.Article{CanonicalURL: "https://example.com/story-b"}

	if deduplication.Fingerprint(a) == deduplication.Fingerprint(b) {
		t.Errorf("expected different fingerprints for different canonical URLs")
	}
}
