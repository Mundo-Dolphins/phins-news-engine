package fetcherwiring_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)        {}
func (noopLogger) Info(string, ...any)         {}
func (noopLogger) Warn(string, ...any)         {}
func (noopLogger) Error(string, error, ...any) {}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

const rssFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Test Feed</title>
<item><title>Isotopes sign new player</title><link>https://example.com/a</link>
<pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>
</channel></rss>`

func baseConfig() *config.Config {
	return &config.Config{
		Telegram: config.TelegramConfig{ChannelID: "-1001234567890", BotToken: "test-token"},
		Fetch:    config.FetchConfig{GlobalConcurrency: 4, FanInBuffer: 16, SourcesPath: "unused-in-tests"},
		// HTTP must be non-zero: rssclient.Fetch bounds every response read
		// to cfg.ResponseLimitBytes+1 bytes, so a zero-value HTTPConfig (as
		// this fixture originally omitted) makes every RSS/Reddit fetch in
		// these tests fail with "response exceeds 0 bytes" — unrelated to
		// what these tests are actually exercising (category activation).
		// AllowPrivateHosts: the feeds in these tests are loopback
		// httptest servers, which the default host policy refuses.
		HTTP: config.HTTPConfig{TimeoutSeconds: 15, MaxResponseBytes: 10 << 20, AllowPrivateHosts: true},
	}
}

func TestBuild_RSSEnabledScrapingServiceDisabled_FetchesArticles(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedXML))
	}))
	defer server.Close()

	cfg := baseConfig()
	cfg.FetchSources.RSS = config.SourceCategoryConfig{Enabled: true}

	catalog := &sourcesfile.Sources{
		RSSFeeds: []sourcesfile.RSSFeedConfig{{
			Name: "Test Feed", URL: server.URL + "/feed.xml", IntervalMinutes: 5,
			MaxItemsPerCycle: 20, MaxAgeHours: 999999, Enabled: true,
		}},
	}

	fetcher, report, err := fetcherwiring.Build(cfg, catalog, config.ProfileLocal, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	articles, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("Fetch() returned %d articles, want 1 — RSS runs without the Scraping Service", len(articles))
	}
	if hits == 0 {
		t.Error("RSS test server received 0 requests, want at least 1")
	}

	var rssEntry *fetcherwiring.CapabilityEntry
	for i := range report.Entries {
		if report.Entries[i].Category == "rss" {
			rssEntry = &report.Entries[i]
		}
	}
	if rssEntry == nil || !rssEntry.Active {
		t.Errorf("report rss entry = %+v, want Active=true", rssEntry)
	}
}

func TestBuild_CategoryDisabled_IsInactiveWithReason(t *testing.T) {
	cfg := baseConfig() // fetch_sources all zero-value (disabled)
	catalog := &sourcesfile.Sources{
		RSSFeeds: []sourcesfile.RSSFeedConfig{{Name: "Feed", URL: "https://x/feed.xml", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 24, Enabled: true}},
		Reddit:   sourcesfile.RedditConfig{Feeds: []sourcesfile.RedditFeedConfig{{Subreddit: "sprisotopes", IntervalMinutes: 10, MaxItemsPerCycle: 15, MaxAgeHours: 48, Enabled: true}}},
		Bluesky:  sourcesfile.BlueskyConfig{Accounts: []sourcesfile.BlueskyAccountConfig{{Handle: "x.bsky.social", IntervalMinutes: 5, MaxItemsPerCycle: 10, MaxAgeHours: 24, Enabled: true}}},
	}
	_, report, err := fetcherwiring.Build(cfg, catalog, config.ProfileLocal, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
	if err == nil {
		t.Fatal("Build() error = nil, want error: zero active sources")
	}
	for _, cat := range []string{"rss", "reddit", "bluesky"} {
		found := false
		for _, e := range report.Entries {
			if e.Category == cat {
				found = true
				if e.Active {
					t.Errorf("category %s Active = true, want false", cat)
				}
				if !strings.Contains(e.Reason, "enabled=false") {
					t.Errorf("category %s Reason = %q, want it to mention enabled=false", cat, e.Reason)
				}
			}
		}
		if !found {
			t.Errorf("report missing entry for category %s", cat)
		}
	}
}

func TestBuild_ZeroActiveSources_IsAlwaysFatal(t *testing.T) {
	cfg := baseConfig() // nothing enabled anywhere
	catalog := &sourcesfile.Sources{}
	for _, p := range []config.Profile{config.ProfileLocal, config.ProfileTest, config.ProfileProduction} {
		_, _, err := fetcherwiring.Build(cfg, catalog, p, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
		if err == nil {
			t.Errorf("Build(%v) error = nil, want fatal error for zero active sources", p)
		}
	}
}

func TestBuild_OneDisabledCatalogEntryDoesNotDisableSiblings(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFeedXML))
	}))
	defer server.Close()

	cfg := baseConfig()
	cfg.FetchSources.RSS = config.SourceCategoryConfig{Enabled: true}
	catalog := &sourcesfile.Sources{
		RSSFeeds: []sourcesfile.RSSFeedConfig{
			{Name: "Disabled Feed", Enabled: false},
			{Name: "Active Feed", URL: server.URL + "/feed.xml", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 999999, Enabled: true},
		},
	}
	fetcher, _, err := fetcherwiring.Build(cfg, catalog, config.ProfileLocal, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	articles, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(articles) != 1 {
		t.Errorf("Fetch() returned %d articles, want 1 (only the enabled entry)", len(articles))
	}
}

func containsActive(report fetcherwiring.Report, category string) bool {
	for _, e := range report.Entries {
		if e.Category == category {
			return e.Active
		}
	}
	return false
}

func TestBuild_YouTubeEnabledWithoutBridge_DepMissing(t *testing.T) {
	cfg := baseConfig()
	cfg.FetchSources.YouTube = config.SourceCategoryConfig{Enabled: true}
	cfg.FetchSources.RSS = config.SourceCategoryConfig{Enabled: true}
	catalog := &sourcesfile.Sources{
		RSSFeeds: []sourcesfile.RSSFeedConfig{{Name: "Feed", URL: "https://x/feed.xml", IntervalMinutes: 5, MaxItemsPerCycle: 20, MaxAgeHours: 24, Enabled: true}},
		YouTube:  sourcesfile.YouTubeConfig{RSSBaseURL: "", Accounts: []sourcesfile.YouTubeAccountConfig{{Name: "Springfield Isotopes", ChannelID: "UCreal12345", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 72, Enabled: true}}},
	}
	_, _, err := fetcherwiring.Build(cfg, catalog, config.ProfileTest, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
	if err == nil {
		t.Fatal("Build(test) error = nil, want fatal error: youtube enabled without rss_base_url")
	}
}

func TestInvalidYouTubeChannelID(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		wantBad   bool
	}{
		{"known placeholder", "UCxxxxxxxx", true},
		{"generic UCx+ pattern", "UCxxxx", true},
		{"empty", "", true},
		{"real-looking id", "UCreal1234567890abcdef", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, bad := fetcherwiring.InvalidYouTubeChannelIDForTest(tt.channelID)
			if bad != tt.wantBad {
				t.Errorf("invalidYouTubeChannelID(%q) bad = %v, want %v", tt.channelID, bad, tt.wantBad)
			}
		})
	}
}

func TestBuild_ProductionSkipsPlaceholderYouTubeChannel(t *testing.T) {
	cfg := baseConfig()
	cfg.FetchSources.YouTube = config.SourceCategoryConfig{Enabled: true}
	catalog := &sourcesfile.Sources{
		YouTube: sourcesfile.YouTubeConfig{RSSBaseURL: "https://bridge.internal.example.com", Accounts: []sourcesfile.YouTubeAccountConfig{
			{Name: "Placeholder", ChannelID: "UCxxxxxxxx", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 72, Enabled: true},
			{Name: "Real", ChannelID: "UCreal1234567890abcdef", IntervalMinutes: 30, MaxItemsPerCycle: 10, MaxAgeHours: 72, Enabled: true},
		}},
	}
	_, report, err := fetcherwiring.Build(cfg, catalog, config.ProfileProduction, fixedClock{t: time.Now()}, noopLogger{}, fetcherwiring.Extensions{})
	if err != nil {
		t.Fatalf("Build(production) error = %v, want nil — one real channel keeps youtube active", err)
	}
	if !containsActive(report, "youtube") {
		t.Error("youtube not active, want active — the real channel should still build")
	}
}

func TestRSSClientConfig_UsesConfiguredHTTPTimeoutAndResponseLimit(t *testing.T) {
	cfg := &config.Config{HTTP: config.HTTPConfig{TimeoutSeconds: 42, MaxResponseBytes: 999}}
	got := fetcherwiring.RSSClientConfig(cfg)
	if got.Timeout != 42*time.Second {
		t.Errorf("Timeout = %v, want 42s", got.Timeout)
	}
	if got.ResponseLimitBytes != 999 {
		t.Errorf("ResponseLimitBytes = %d, want 999", got.ResponseLimitBytes)
	}
}

func TestRSSClientConfig_BlocksPrivateHostsUnlessConfigured(t *testing.T) {
	cfg := &config.Config{HTTP: config.HTTPConfig{TimeoutSeconds: 1, MaxResponseBytes: 1}}
	if fetcherwiring.RSSClientConfig(cfg).AllowPrivateHosts {
		t.Fatal("AllowPrivateHosts defaults to true, want false")
	}
	cfg.HTTP.AllowPrivateHosts = true
	if !fetcherwiring.RSSClientConfig(cfg).AllowPrivateHosts {
		t.Fatal("http.allow_private_hosts=true not propagated")
	}
}
