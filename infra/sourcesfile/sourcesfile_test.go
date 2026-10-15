package sourcesfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestWebsiteConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.WebsiteConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.WebsiteConfig{Name: "Official", URL: "https://x", Parser: "official", RenderMode: "auto", IntervalMinutes: 10, MaxItemsPerCycle: 15, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.WebsiteConfig{Name: "Official", Enabled: false}, false},
		{"missing name always invalid", sourcesfile.WebsiteConfig{Enabled: false}, true},
		{"enabled missing url", sourcesfile.WebsiteConfig{Name: "Official", Parser: "official", IntervalMinutes: 10, MaxItemsPerCycle: 15, Enabled: true}, true},
		{"enabled missing parser", sourcesfile.WebsiteConfig{Name: "Official", URL: "https://x", IntervalMinutes: 10, MaxItemsPerCycle: 15, Enabled: true}, true},
		{"enabled invalid render_mode", sourcesfile.WebsiteConfig{Name: "Official", URL: "https://x", Parser: "official", RenderMode: "warp", IntervalMinutes: 10, MaxItemsPerCycle: 15, Enabled: true}, true},
		{"enabled zero interval_minutes", sourcesfile.WebsiteConfig{Name: "Official", URL: "https://x", Parser: "official", IntervalMinutes: 0, MaxItemsPerCycle: 15, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.WebsiteConfig{Name: "Official", URL: "https://x", Parser: "official", IntervalMinutes: 10, MaxItemsPerCycle: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRSSFeedConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.RSSFeedConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.RSSFeedConfig{Name: "Feed", URL: "https://x/feed.xml", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 24, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.RSSFeedConfig{Name: "Feed", Enabled: false}, false},
		{"missing name always invalid", sourcesfile.RSSFeedConfig{Enabled: false}, true},
		{"enabled missing url", sourcesfile.RSSFeedConfig{Name: "Feed", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero interval_minutes", sourcesfile.RSSFeedConfig{Name: "Feed", URL: "https://x", IntervalMinutes: 0, MaxItemsPerCycle: 20, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.RSSFeedConfig{Name: "Feed", URL: "https://x", IntervalMinutes: 5, MaxItemsPerCycle: 0, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.RSSFeedConfig{Name: "Feed", URL: "https://x", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesWebsitesAndRSSFeeds(t *testing.T) {
	const yamlContent = `
websites:
  - name: "Springfield Isotopes Official"
    url: "https://www.isotopes.example.com"
    parser: official
    source_type: hard_news
    render_mode: auto
    interval_minutes: 10
    max_items_per_cycle: 15
    enabled: true
rss_feeds:
  - name: "Example Isotopes Feed"
    url: "https://example.com/feed.xml"
    source_type: hard_news
    interval_minutes: 5
    max_items_per_cycle: 20
    max_age_hours: 24
    blocked_keywords: []
    required_keywords: []
    blocked_authors: []
    enabled: true
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.Websites) != 1 || s.Websites[0].Name != "Springfield Isotopes Official" {
		t.Errorf("Websites = %+v", s.Websites)
	}
	if len(s.RSSFeeds) != 1 || s.RSSFeeds[0].MaxAgeHours != 24 {
		t.Errorf("RSSFeeds = %+v", s.RSSFeeds)
	}
}

func TestLoad_RejectsDuplicateWebsiteNames(t *testing.T) {
	const yamlContent = `
websites:
  - name: "Official"
    url: "https://a"
    parser: official
    interval_minutes: 10
    max_items_per_cycle: 15
    enabled: true
  - name: "Official"
    url: "https://b"
    parser: official
    interval_minutes: 10
    max_items_per_cycle: 15
    enabled: true
`
	path := writeFixture(t, yamlContent)
	if _, err := sourcesfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for duplicate website name")
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	const yamlContent = `
websites: []
not_a_real_section: true
`
	path := writeFixture(t, yamlContent)
	if _, err := sourcesfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for unknown top-level key")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := sourcesfile.Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("Load() error = nil, want error for missing file")
	}
}

func TestRedditFeedConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.RedditFeedConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", Mode: "new", IntervalMinutes: 10, MaxItemsPerCycle: 15, MaxAgeHours: 48, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", Enabled: false}, false},
		{"missing subreddit always invalid", sourcesfile.RedditFeedConfig{Enabled: false}, true},
		{"enabled invalid mode", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", Mode: "trending", IntervalMinutes: 10, MaxItemsPerCycle: 15, MaxAgeHours: 48, Enabled: true}, true},
		{"enabled zero interval_minutes", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", IntervalMinutes: 0, MaxItemsPerCycle: 15, MaxAgeHours: 48, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", IntervalMinutes: 10, MaxItemsPerCycle: 0, MaxAgeHours: 48, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.RedditFeedConfig{Subreddit: "sprisotopes", IntervalMinutes: 10, MaxItemsPerCycle: 15, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesRedditSection(t *testing.T) {
	const yamlContent = `
reddit:
  feeds:
    - subreddit: sprisotopes
      mode: new
      source_type: community
      interval_minutes: 10
      max_items_per_cycle: 15
      max_age_hours: 48
      enabled: true
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.Reddit.Feeds) != 1 || s.Reddit.Feeds[0].Subreddit != "sprisotopes" {
		t.Errorf("Reddit.Feeds = %+v", s.Reddit.Feeds)
	}
}

func TestBlueskyAccountConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.BlueskyAccountConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.BlueskyAccountConfig{Handle: "isotopes.bsky.social", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.BlueskyAccountConfig{Handle: "isotopes.bsky.social", Enabled: false}, false},
		{"missing handle always invalid", sourcesfile.BlueskyAccountConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.BlueskyAccountConfig{Handle: "isotopes.bsky.social", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.BlueskyAccountConfig{Handle: "isotopes.bsky.social", IntervalMinutes: 5, MaxItemsPerCycle: 0, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.BlueskyAccountConfig{Handle: "isotopes.bsky.social", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBlueskyFeedConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.BlueskyFeedConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.BlueskyFeedConfig{URI: "at://did:plc:x/app.bsky.feed.generator/y", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.BlueskyFeedConfig{URI: "at://did:plc:x/app.bsky.feed.generator/y", Enabled: false}, false},
		{"missing uri always invalid", sourcesfile.BlueskyFeedConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.BlueskyFeedConfig{URI: "at://did:plc:x/app.bsky.feed.generator/y", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.BlueskyFeedConfig{URI: "at://did:plc:x/app.bsky.feed.generator/y", IntervalMinutes: 5, MaxItemsPerCycle: 0, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.BlueskyFeedConfig{URI: "at://did:plc:x/app.bsky.feed.generator/y", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesBlueskySection(t *testing.T) {
	const yamlContent = `
bluesky:
  accounts:
    - handle: "isotopes.bsky.social"
      name: "Springfield Isotopes"
      source_type: hard_news
      interval_minutes: 5
      max_items_per_cycle: 10
      max_age_hours: 24
      blocked_urls: []
      blocked_keywords: []
      skip_authors: []
      enabled: true
  feeds: []
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.Bluesky.Accounts) != 1 || s.Bluesky.Accounts[0].Handle != "isotopes.bsky.social" {
		t.Errorf("Bluesky.Accounts = %+v", s.Bluesky.Accounts)
	}
}

func TestTwitterConfig_Validate(t *testing.T) {
	if err := (sourcesfile.TwitterConfig{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for an entirely empty section", err)
	}

	withAccountNoBaseURL := sourcesfile.TwitterConfig{
		Accounts: []sourcesfile.TwitterAccountConfig{{Handle: "SprIsotopes", IntervalMinutes: 3, MaxItemsPerCycle: 10, MaxAgeHours: 24}},
	}
	if err := withAccountNoBaseURL.Validate(); err == nil {
		t.Error("Validate() error = nil, want error: proxy_base_url required once an account is configured")
	}

	valid := sourcesfile.TwitterConfig{
		ProxyBaseURL: "http://127.0.0.1:8090",
		Accounts:     []sourcesfile.TwitterAccountConfig{{Handle: "SprIsotopes", IntervalMinutes: 3, MaxItemsPerCycle: 10, MaxAgeHours: 24}},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestTwitterAccountConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.TwitterAccountConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.TwitterAccountConfig{Handle: "SprIsotopes", IntervalMinutes: 3, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.TwitterAccountConfig{Handle: "SprIsotopes", Enabled: false}, false},
		{"missing handle always invalid", sourcesfile.TwitterAccountConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.TwitterAccountConfig{Handle: "SprIsotopes", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.TwitterAccountConfig{Handle: "SprIsotopes", IntervalMinutes: 3, MaxItemsPerCycle: 0, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.TwitterAccountConfig{Handle: "SprIsotopes", IntervalMinutes: 3, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTwitterSearchConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.TwitterSearchConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.TwitterSearchConfig{Hashtag: "Isotopes", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.TwitterSearchConfig{Hashtag: "Isotopes", Enabled: false}, false},
		{"missing hashtag always invalid", sourcesfile.TwitterSearchConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.TwitterSearchConfig{Hashtag: "Isotopes", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.TwitterSearchConfig{Hashtag: "Isotopes", IntervalMinutes: 5, MaxItemsPerCycle: 0, MaxAgeHours: 24, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.TwitterSearchConfig{Hashtag: "Isotopes", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesTwitterSection(t *testing.T) {
	const yamlContent = `
twitter:
  proxy_base_url: "http://127.0.0.1:8090"
  proxy_fallback_urls: []
  accounts:
    - handle: "SprIsotopes"
      name: "Springfield Isotopes"
      source_type: hard_news
      interval_minutes: 3
      max_items_per_cycle: 10
      max_age_hours: 24
      skip_authors: []
      blocked_urls: []
      blocked_keywords: []
      enabled: true
  searches: []
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if s.Twitter.ProxyBaseURL != "http://127.0.0.1:8090" {
		t.Errorf("Twitter.ProxyBaseURL = %q", s.Twitter.ProxyBaseURL)
	}
	if len(s.Twitter.Accounts) != 1 || s.Twitter.Accounts[0].Handle != "SprIsotopes" {
		t.Errorf("Twitter.Accounts = %+v", s.Twitter.Accounts)
	}
}

func TestInstagramConfig_Validate(t *testing.T) {
	if err := (sourcesfile.InstagramConfig{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for an entirely empty section", err)
	}
	withAccountNoBaseURL := sourcesfile.InstagramConfig{
		Accounts: []sourcesfile.InstagramAccountConfig{{Handle: "sprisotopes", IntervalMinutes: 15, MaxItemsPerCycle: 10, MaxAgeHours: 48}},
	}
	if err := withAccountNoBaseURL.Validate(); err == nil {
		t.Error("Validate() error = nil, want error: rss_base_url required once an account is configured")
	}
}

func TestInstagramAccountConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.InstagramAccountConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.InstagramAccountConfig{Handle: "sprisotopes", IntervalMinutes: 15, MaxItemsPerCycle: 10, MaxAgeHours: 48, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.InstagramAccountConfig{Handle: "sprisotopes", Enabled: false}, false},
		{"missing handle always invalid", sourcesfile.InstagramAccountConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.InstagramAccountConfig{Handle: "sprisotopes", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 48, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.InstagramAccountConfig{Handle: "sprisotopes", IntervalMinutes: 15, MaxItemsPerCycle: 0, MaxAgeHours: 48, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.InstagramAccountConfig{Handle: "sprisotopes", IntervalMinutes: 15, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesInstagramSection(t *testing.T) {
	const yamlContent = `
instagram:
  rss_base_url: "http://127.0.0.1:5000"
  rss_path_template: "/user/%s"
  accounts:
    - handle: "sprisotopes"
      name: "Springfield Isotopes"
      source_type: hard_news
      interval_minutes: 15
      max_items_per_cycle: 10
      max_age_hours: 48
      enabled: true
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.Instagram.Accounts) != 1 || s.Instagram.Accounts[0].Handle != "sprisotopes" {
		t.Errorf("Instagram.Accounts = %+v", s.Instagram.Accounts)
	}
}

func TestYouTubeConfig_Validate(t *testing.T) {
	if err := (sourcesfile.YouTubeConfig{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for an entirely empty section", err)
	}
	withAccountNoBaseURL := sourcesfile.YouTubeConfig{
		Accounts: []sourcesfile.YouTubeAccountConfig{{ChannelID: "UCxxxx", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 72}},
	}
	if err := withAccountNoBaseURL.Validate(); err == nil {
		t.Error("Validate() error = nil, want error: rss_base_url required once an account is configured")
	}
}

func TestYouTubeAccountConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     sourcesfile.YouTubeAccountConfig
		wantErr bool
	}{
		{"valid enabled", sourcesfile.YouTubeAccountConfig{Name: "Springfield Isotopes", ChannelID: "UCxxxx", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 72, Enabled: true}, false},
		{"disabled tolerates empty fields", sourcesfile.YouTubeAccountConfig{ChannelID: "UCxxxx", Enabled: false}, false},
		{"missing channel_id always invalid", sourcesfile.YouTubeAccountConfig{Enabled: false}, true},
		{"enabled zero interval_minutes", sourcesfile.YouTubeAccountConfig{Name: "Springfield Isotopes", ChannelID: "UCxxxx", IntervalMinutes: 0, MaxItemsPerCycle: 10, MaxAgeHours: 72, Enabled: true}, true},
		{"enabled zero max_items_per_cycle", sourcesfile.YouTubeAccountConfig{Name: "Springfield Isotopes", ChannelID: "UCxxxx", IntervalMinutes: 30, MaxItemsPerCycle: 0, MaxAgeHours: 72, Enabled: true}, true},
		{"enabled zero max_age_hours", sourcesfile.YouTubeAccountConfig{Name: "Springfield Isotopes", ChannelID: "UCxxxx", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 0, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesYouTubeSection(t *testing.T) {
	const yamlContent = `
youtube:
  rss_base_url: "http://localhost:7000"
  accounts:
    - name: "Springfield Isotopes"
      channel_id: "UCxxxxxxxx"
      source_type: hard_news
      interval_minutes: 30
      max_items_per_cycle: 10
      max_age_hours: 72
      enabled: true
`
	path := writeFixture(t, yamlContent)
	s, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.YouTube.Accounts) != 1 || s.YouTube.Accounts[0].ChannelID != "UCxxxxxxxx" {
		t.Errorf("YouTube.Accounts = %+v", s.YouTube.Accounts)
	}
}

func TestSources_IDs_BuildsNamespacedIDs(t *testing.T) {
	s := sourcesfile.Sources{
		Twitter:   sourcesfile.TwitterConfig{Accounts: []sourcesfile.TwitterAccountConfig{{Handle: "SprIsotopes", Enabled: true, IntervalMinutes: 1, MaxItemsPerCycle: 1, MaxAgeHours: 1}}},
		Instagram: sourcesfile.InstagramConfig{Accounts: []sourcesfile.InstagramAccountConfig{{Handle: "sprisotopes", Enabled: true, IntervalMinutes: 1, MaxItemsPerCycle: 1, MaxAgeHours: 1}}},
	}
	ids, err := s.IDs()
	if err != nil {
		t.Fatalf("IDs(): %v", err)
	}
	got := make(map[string]bool, len(ids))
	for _, id := range ids {
		got[id.String()] = true
	}
	if !got["twitter:sprisotopes"] || !got["instagram:sprisotopes"] {
		t.Errorf("IDs() = %v, want both twitter:sprisotopes and instagram:sprisotopes", got)
	}
}

func TestSources_Validate_RejectsCrossPlatformIDCollision(t *testing.T) {
	// Two RSS feeds whose names slug to the same identifier collide
	// globally even though sourcesfile's existing per-section duplicate-name
	// check (on the raw Name field) wouldn't catch a slug collision between
	// differently-cased/punctuated names.
	s := sourcesfile.Sources{
		RSSFeeds: []sourcesfile.RSSFeedConfig{
			{Name: "Isotopes Feed", Enabled: false},
			{Name: "isotopes-feed", Enabled: false},
		},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for colliding source IDs")
	}
}

func TestSources_Validate_YouTubePlaceholderPasses(t *testing.T) {
	// The real committed sources.yml placeholder: disabled, charset-valid,
	// non-standard length. Must pass — see spec §4.
	s := sourcesfile.Sources{
		YouTube: sourcesfile.YouTubeConfig{
			RSSBaseURL: "http://localhost:7000",
			Accounts:   []sourcesfile.YouTubeAccountConfig{{Name: "Springfield Isotopes", ChannelID: "UCxxxxxxxx", Enabled: false}},
		},
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for the documented YouTube placeholder", err)
	}
}

func TestSources_Validate_RejectsInvalidIdentifierEvenWhenDisabled(t *testing.T) {
	s := sourcesfile.Sources{
		Twitter: sourcesfile.TwitterConfig{Accounts: []sourcesfile.TwitterAccountConfig{{Handle: "has a space", Enabled: false}}},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for an invalid handle even on a disabled source")
	}
}

func TestWebsiteConfig_SkipBodyExtractionDefaultsFalse(t *testing.T) {
	const yamlContent = `
websites:
  - name: "Site"
    url: "https://site.example"
    parser: official
    source_type: hard_news
    render_mode: auto
    interval_minutes: 10
    max_items_per_cycle: 10
    enabled: true
`
	path := writeFixture(t, yamlContent)
	sources, err := sourcesfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if sources.Websites[0].SkipBodyExtraction {
		t.Error("SkipBodyExtraction = true, want false when the yaml key is omitted")
	}
}
