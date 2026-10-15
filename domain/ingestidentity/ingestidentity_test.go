package ingestidentity_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
)

func TestResolve_PrefersRemoteIDOverCanonicalURL(t *testing.T) {
	id := ingestidentity.Resolve("rss", "official-source", "guid-123", "https://example.com/a")
	if id.Basis != ingestidentity.BasisRemoteID {
		t.Fatalf("Basis = %q, want %q", id.Basis, ingestidentity.BasisRemoteID)
	}
	if id.Platform != "rss" || id.SourceID != "official-source" || id.Algorithm != ingestidentity.Algorithm {
		t.Fatalf("id = %+v, want platform=rss source=official-source algorithm=%s", id, ingestidentity.Algorithm)
	}
	if id.Key == "" {
		t.Fatal("Key is empty, want a derived hash")
	}
}

func TestResolve_FallsBackToCanonicalURLWhenRemoteIDEmpty(t *testing.T) {
	id := ingestidentity.Resolve("web", "official-site", "", "https://example.com/a")
	if id.Basis != ingestidentity.BasisCanonicalURL {
		t.Fatalf("Basis = %q, want %q", id.Basis, ingestidentity.BasisCanonicalURL)
	}
	if id.Key == "" {
		t.Fatal("Key is empty, want a derived hash")
	}
}

func TestResolve_EmptyRemoteIDAndURLReturnsZeroValue(t *testing.T) {
	id := ingestidentity.Resolve("web", "official-site", "", "")
	if id.Key != "" || id.Basis != "" {
		t.Fatalf("id = %+v, want zero value", id)
	}
}

func TestResolve_SameRawIDAcrossDifferentSourcesNeverCollides(t *testing.T) {
	a := ingestidentity.Resolve("rss", "feed-a", "123", "")
	b := ingestidentity.Resolve("rss", "feed-b", "123", "")
	if a.Key == b.Key {
		t.Fatalf("feed-a and feed-b both resolved to key %q, want distinct keys", a.Key)
	}
}

func TestResolve_SameRawIDAcrossDifferentPlatformsNeverCollides(t *testing.T) {
	a := ingestidentity.Resolve("rss", "shared-source", "123", "")
	b := ingestidentity.Resolve("twitter", "shared-source", "123", "")
	if a.Key == b.Key {
		t.Fatalf("rss and twitter both resolved to key %q, want distinct keys", a.Key)
	}
}

func TestResolve_RemoteIDBasisAndCanonicalURLBasisNeverCollideOnEqualInput(t *testing.T) {
	// A pathological feed where the GUID happens to equal the URL string.
	shared := "https://example.com/a"
	byID := ingestidentity.Resolve("rss", "feed-a", shared, "https://example.com/other")
	byURL := ingestidentity.Resolve("rss", "feed-a", "", shared)
	if byID.Key == byURL.Key {
		t.Fatalf("remote-id basis and canonical-url basis both resolved to key %q, want distinct keys", byID.Key)
	}
}

func TestResolve_IsDeterministic(t *testing.T) {
	a := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/a")
	b := ingestidentity.Resolve("rss", "feed-a", "guid-1", "https://example.com/a")
	if a.Key != b.Key {
		t.Fatalf("Resolve is not deterministic: %q != %q", a.Key, b.Key)
	}
}

func TestContentHash_StableAcrossIdenticalInput(t *testing.T) {
	a := ingestidentity.ContentHash("https://example.com/a", "Title", "Summary")
	b := ingestidentity.ContentHash("https://example.com/a", "Title", "Summary")
	if a != b {
		t.Fatalf("ContentHash not stable: %q != %q", a, b)
	}
}

func TestContentHash_ChangesWhenTitleChanges(t *testing.T) {
	a := ingestidentity.ContentHash("https://example.com/a", "Original Title", "Summary")
	b := ingestidentity.ContentHash("https://example.com/a", "Corrected Title", "Summary")
	if a == b {
		t.Fatal("ContentHash unchanged after title edit, want a different hash")
	}
}

func TestContentHash_IgnoresFieldBoundaryAmbiguity(t *testing.T) {
	// "ab"+"c" must not hash the same as "a"+"bc" — guards against a naive
	// string-concatenation implementation.
	a := ingestidentity.ContentHash("u", "ab", "c")
	b := ingestidentity.ContentHash("u", "a", "bc")
	if a == b {
		t.Fatal("ContentHash collides across a field-boundary shift, want distinct hashes")
	}
}

func TestIsCollision_RemoteIDBasisWithDifferentHost(t *testing.T) {
	if !ingestidentity.IsCollision(ingestidentity.BasisRemoteID, "https://old-site.example/x", "https://new-site.example/y") {
		t.Fatal("want collision: same remote id, different host")
	}
}

func TestIsCollision_RemoteIDBasisWithSameHostIsNotACollision(t *testing.T) {
	if ingestidentity.IsCollision(ingestidentity.BasisRemoteID, "https://example.com/x", "https://example.com/x-corrected") {
		t.Fatal("want no collision: same remote id, same host, just an edit")
	}
}

func TestIsCollision_CanonicalURLBasisNeverFlagsCollision(t *testing.T) {
	if ingestidentity.IsCollision(ingestidentity.BasisCanonicalURL, "https://old.example/x", "https://new.example/y") {
		t.Fatal("want no collision: the URL itself is the identity under this basis, a host change is just a different identity")
	}
}

func TestIsCollision_EmptyPreviousURLIsConservativelyNotACollision(t *testing.T) {
	if ingestidentity.IsCollision(ingestidentity.BasisRemoteID, "", "https://example.com/x") {
		t.Fatal("want no collision: no previous URL recorded (e.g. backfilled row) is not solid evidence")
	}
}
