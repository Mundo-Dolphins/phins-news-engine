package source_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestNewID_PerPlatformNormalization(t *testing.T) {
	tests := []struct {
		name     string
		platform source.Type
		raw      string
		want     string
		wantErr  bool
	}{
		{"twitter lowercases and strips @", source.TypeTwitter, "@SprIsotopes", "twitter:sprisotopes", false},
		{"twitter plain handle", source.TypeTwitter, "SprIsotopes", "twitter:sprisotopes", false},
		{"twitter hashtag search shape", source.TypeTwitter, "hashtag-Isotopes", "twitter:hashtag-isotopes", false},
		{"twitter invalid charset", source.TypeTwitter, "springfield isotopes", "", true},
		{"twitter too long", source.TypeTwitter, "a1234567890123456", "", true},
		{"instagram lowercases and strips @", source.TypeInstagram, "@SprIsotopes", "instagram:sprisotopes", false},
		{"instagram allows periods and underscores", source.TypeInstagram, "springfield.isotopes_official", "instagram:springfield.isotopes_official", false},
		{"bluesky keeps domain suffix whole", source.TypeBluesky, "Isotopes.bsky.social", "bluesky:isotopes.bsky.social", false},
		{"bluesky custom feed shape", source.TypeBluesky, "feed-SomeFeed", "bluesky:feed-somefeed", false},
		{"bluesky bare handle with no domain is invalid", source.TypeBluesky, "sprisotopes", "", true},
		{"youtube preserves case exactly", source.TypeYouTube, "UCxxxxxxxx", "youtube:UCxxxxxxxx", false},
		{"youtube rejects lowercase uc prefix", source.TypeYouTube, "ucabc", "", true},
		{"youtube rejects bad charset", source.TypeYouTube, "UC abc", "", true},
		{"reddit shape and lowercasing", source.TypeReddit, "reddit-SprIsotopes", "reddit:reddit-sprisotopes", false},
		{"reddit rejects bare subreddit with no prefix", source.TypeReddit, "sprisotopes", "", true},
		{"rss slugs the feed name", source.TypeRSS, "Example Isotopes Feed", "rss:example-isotopes-feed", false},
		{"web slugs the site name", source.TypeWeb, "Springfield Isotopes Official", "web:springfield-isotopes-official", false},
		{"empty identifier is invalid", source.TypeTwitter, "", "", true},
		{"unknown platform is invalid", source.Type("carrier-pigeon"), "x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := source.NewID(tt.platform, tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewID(%q, %q) error = %v, wantErr %v", tt.platform, tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.String() != tt.want {
				t.Errorf("NewID(%q, %q) = %q, want %q", tt.platform, tt.raw, got.String(), tt.want)
			}
			if got.Platform() != tt.platform {
				t.Errorf("Platform() = %q, want %q", got.Platform(), tt.platform)
			}
		})
	}
}

func TestNewID_TwitterInstagramNeverCollide(t *testing.T) {
	tw, err := source.NewID(source.TypeTwitter, "SprIsotopes")
	if err != nil {
		t.Fatalf("twitter NewID: %v", err)
	}
	ig, err := source.NewID(source.TypeInstagram, "sprisotopes")
	if err != nil {
		t.Fatalf("instagram NewID: %v", err)
	}
	if tw.String() == ig.String() {
		t.Fatalf("Twitter and Instagram IDs collided: %q == %q", tw.String(), ig.String())
	}
}

func TestParseID_RoundTripsWithNewID(t *testing.T) {
	id, err := source.NewID(source.TypeTwitter, "@SprIsotopes")
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	parsed, err := source.ParseID(id.String())
	if err != nil {
		t.Fatalf("ParseID(%q): %v", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParseID(NewID(...).String()) = %+v, want %+v", parsed, id)
	}
}

func TestParseID_RejectsMalformed(t *testing.T) {
	for _, raw := range []string{"", "notaplatform:x", "twitter", "twitter:", ":sprisotopes", "twitter:springfield isotopes"} {
		if _, err := source.ParseID(raw); err == nil {
			t.Errorf("ParseID(%q) = nil error, want error", raw)
		}
	}
}

func TestID_TextMarshalUnmarshal(t *testing.T) {
	id, err := source.NewID(source.TypeTwitter, "SprIsotopes")
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	text, err := id.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(text) != "twitter:sprisotopes" {
		t.Errorf("MarshalText() = %q, want %q", text, "twitter:sprisotopes")
	}
	var got source.ID
	if err := got.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if got != id {
		t.Errorf("UnmarshalText round trip = %+v, want %+v", got, id)
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Example Isotopes Feed", "example-isotopes-feed"},
		{"Springfield Isotopes Official", "springfield-isotopes-official"},
		{"  Spaced  Out  ", "spaced-out"},
	}
	for _, tt := range tests {
		if got := source.Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
