package normalization_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

func defaultURLConfig() normalization.URLConfig {
	return normalization.URLConfig{
		TrackingParams:    []string{"utm_source", "utm_medium", "fbclid"},
		StripDefaultPorts: true,
		TrailingSlash:     "strip",
	}
}

func TestNormalizeURL_LowercasesSchemeAndHost(t *testing.T) {
	got, err := normalization.NormalizeURL("HTTPS://Example.COM/Story", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/Story" {
		t.Errorf("got %q, want %q", got, "https://example.com/Story")
	}
}

func TestNormalizeURL_RemovesFragment(t *testing.T) {
	got, err := normalization.NormalizeURL("https://example.com/story#section-2", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story" {
		t.Errorf("got %q, want no fragment", got)
	}
}

func TestNormalizeURL_StripsDefaultPorts(t *testing.T) {
	got, err := normalization.NormalizeURL("http://example.com:80/story", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "http://example.com/story" {
		t.Errorf("got %q, want port stripped", got)
	}

	got, err = normalization.NormalizeURL("https://example.com:443/story", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story" {
		t.Errorf("got %q, want port stripped", got)
	}
}

func TestNormalizeURL_TrailingSlashPolicyNeverStripsRoot(t *testing.T) {
	got, err := normalization.NormalizeURL("https://example.com/", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/" {
		t.Errorf("got %q, want root slash kept", got)
	}

	got, err = normalization.NormalizeURL("https://example.com/story/", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story" {
		t.Errorf("got %q, want trailing slash stripped on non-root path", got)
	}
}

func TestNormalizeURL_TrailingSlashKeepPolicy(t *testing.T) {
	cfg := defaultURLConfig()
	cfg.TrailingSlash = "keep"
	got, err := normalization.NormalizeURL("https://example.com/story/", cfg, nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story/" {
		t.Errorf("got %q, want trailing slash kept", got)
	}
}

func TestNormalizeURL_RemovesOnlyConfiguredTrackingParams(t *testing.T) {
	got, err := normalization.NormalizeURL(
		"https://example.com/story?utm_source=twitter&id=42&fbclid=abc",
		defaultURLConfig(), nil,
	)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story?id=42" {
		t.Errorf("got %q, want only id=42 kept (unknown params are never removed)", got)
	}
}

func TestNormalizeURL_DomainSpecificIgnoredParams(t *testing.T) {
	cfg := defaultURLConfig()
	cfg.DomainIgnoredParams = map[string][]string{"news.example.com": {"ref"}}
	got, err := normalization.NormalizeURL("https://news.example.com/story?ref=home&id=1", cfg, nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://news.example.com/story?id=1" {
		t.Errorf("got %q, want domain-specific param removed", got)
	}

	got, err = normalization.NormalizeURL("https://other.example.com/story?ref=home", cfg, nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://other.example.com/story?ref=home" {
		t.Errorf("got %q, want ref kept on a different domain", got)
	}
}

func TestNormalizeURL_CanonicalURLIsRespectedWhenAlreadySet(t *testing.T) {
	// The function itself has no notion of "already canonical" — that
	// decision belongs to the caller (ProcessCycle only calls this on
	// OriginalURL when Article.CanonicalURL was empty). This test just
	// pins down that normalizing an already-clean URL is a no-op.
	got, err := normalization.NormalizeURL("https://example.com/story", defaultURLConfig(), nil)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://example.com/story" {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestNormalizeURL_InvalidURLReturnsError(t *testing.T) {
	_, err := normalization.NormalizeURL("http://[::1]:namedport/", defaultURLConfig(), nil)
	if err == nil {
		t.Fatal("NormalizeURL() error = nil, want a parse error")
	}
}

func TestYouTubeResolver_CanonicalizesWatchShortsLiveEmbed(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=abc123&t=30s": "https://www.youtube.com/watch?v=abc123",
		"https://youtu.be/abc123":                      "https://www.youtube.com/watch?v=abc123",
		"https://www.youtube.com/shorts/abc123":        "https://www.youtube.com/watch?v=abc123",
		"https://www.youtube.com/live/abc123":          "https://www.youtube.com/watch?v=abc123",
		"https://www.youtube.com/embed/abc123":         "https://www.youtube.com/watch?v=abc123",
	}
	for in, want := range cases {
		got, ok := normalization.YouTubeResolver(in)
		if !ok {
			t.Errorf("YouTubeResolver(%q) ok = false, want true", in)
			continue
		}
		if got != want {
			t.Errorf("YouTubeResolver(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestYouTubeResolver_IgnoresNonYouTubeURLs(t *testing.T) {
	_, ok := normalization.YouTubeResolver("https://example.com/watch?v=abc123")
	if ok {
		t.Error("YouTubeResolver() ok = true, want false for a non-YouTube host")
	}
}

func TestNormalizeURL_AppliesFirstMatchingResolver(t *testing.T) {
	got, err := normalization.NormalizeURL(
		"https://youtu.be/xyz789?feature=share",
		defaultURLConfig(),
		[]normalization.KnownLinkResolver{normalization.YouTubeResolver},
	)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if got != "https://www.youtube.com/watch?v=xyz789" {
		t.Errorf("got %q, want resolved YouTube canonical form", got)
	}
}
