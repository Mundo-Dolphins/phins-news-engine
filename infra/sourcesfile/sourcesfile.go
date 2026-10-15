// Package sourcesfile loads and validates configs/sources.yml, the content-
// source catalog consumed by adapters/outbound/compositefetcher.
// It is separate from infrastructure/config's configs/settings.yml (process
// behavior), following the same pattern v1 used to split
// settings.yml from feeds.yml.
package sourcesfile

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"gopkg.in/yaml.v3"
)

// Sources is the fully loaded and validated content-source catalog.
type Sources struct {
	Websites  []WebsiteConfig `yaml:"websites"`
	RSSFeeds  []RSSFeedConfig `yaml:"rss_feeds"`
	Reddit    RedditConfig    `yaml:"reddit"`
	Bluesky   BlueskyConfig   `yaml:"bluesky"`
	Twitter   TwitterConfig   `yaml:"twitter"`
	Instagram InstagramConfig `yaml:"instagram"`
	YouTube   YouTubeConfig   `yaml:"youtube"`
}

// IDs returns every source.ID this catalog would construct, in
// declaration order, walking every section regardless of Enabled — the
// single source of truth both Validate (below) and the cross-file
// source-policy validator (app/usecase.
// ValidateSourcePolicies) use for "what sources actually exist."
// Returns an error naming the first entry whose identifier fails its
// platform's format validation (source.NewID) — this applies uniformly
// to enabled and disabled entries, see the design spec §4.
func (s Sources) IDs() ([]source.ID, error) {
	var ids []source.ID
	add := func(platform source.Type, raw string) error {
		id, err := source.NewID(platform, raw)
		if err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}
	for _, w := range s.Websites {
		if err := add(source.TypeWeb, w.Name); err != nil {
			return nil, fmt.Errorf("sourcesfile: websites[%s]: %w", w.Name, err)
		}
	}
	for _, f := range s.RSSFeeds {
		if err := add(source.TypeRSS, f.Name); err != nil {
			return nil, fmt.Errorf("sourcesfile: rss_feeds[%s]: %w", f.Name, err)
		}
	}
	for _, f := range s.Reddit.Feeds {
		if err := add(source.TypeReddit, "reddit-"+f.Subreddit); err != nil {
			return nil, fmt.Errorf("sourcesfile: reddit.feeds[%s]: %w", f.Subreddit, err)
		}
	}
	for _, a := range s.Bluesky.Accounts {
		if err := add(source.TypeBluesky, a.Handle); err != nil {
			return nil, fmt.Errorf("sourcesfile: bluesky.accounts[%s]: %w", a.Handle, err)
		}
	}
	for _, f := range s.Bluesky.Feeds {
		name := f.Name
		if name == "" {
			name = f.URI
		}
		if err := add(source.TypeBluesky, "feed-"+source.Slug(name)); err != nil {
			return nil, fmt.Errorf("sourcesfile: bluesky.feeds[%s]: %w", f.URI, err)
		}
	}
	for _, a := range s.Twitter.Accounts {
		if err := add(source.TypeTwitter, a.Handle); err != nil {
			return nil, fmt.Errorf("sourcesfile: twitter.accounts[%s]: %w", a.Handle, err)
		}
	}
	for _, srch := range s.Twitter.Searches {
		if err := add(source.TypeTwitter, "hashtag-"+srch.Hashtag); err != nil {
			return nil, fmt.Errorf("sourcesfile: twitter.searches[%s]: %w", srch.Hashtag, err)
		}
	}
	for _, l := range s.Twitter.Lists {
		if err := add(source.TypeTwitter, "list-"+l.ID); err != nil {
			return nil, fmt.Errorf("sourcesfile: twitter.lists[%s]: %w", l.ID, err)
		}
	}
	for _, a := range s.Instagram.Accounts {
		if err := add(source.TypeInstagram, a.Handle); err != nil {
			return nil, fmt.Errorf("sourcesfile: instagram.accounts[%s]: %w", a.Handle, err)
		}
	}
	for _, a := range s.YouTube.Accounts {
		if err := add(source.TypeYouTube, a.ChannelID); err != nil {
			return nil, fmt.Errorf("sourcesfile: youtube.accounts[%s]: %w", a.ChannelID, err)
		}
	}
	return ids, nil
}

// WebsiteConfig is one configured website source scraped through the
// Scraping Service (see ADR 0011). Moved here from
// infrastructure/config.WebsiteSourceConfig in Phase 5b.
type WebsiteConfig struct {
	Name             string `yaml:"name"`
	URL              string `yaml:"url"`
	Parser           string `yaml:"parser"`
	SourceType       string `yaml:"source_type"`
	RenderMode       string `yaml:"render_mode"`
	IntervalMinutes  int    `yaml:"interval_minutes"`
	MaxItemsPerCycle int    `yaml:"max_items_per_cycle"`
	Enabled          bool   `yaml:"enabled"`
	// SkipBodyExtraction opts a site out of article-page body extraction
	// (website.Fetcher.fetchSite) — e.g. its robots.txt disallows article
	// paths, its ToS forbids scraping full text, or the readability
	// extractor misfires on its layout. Default false (extraction runs)
	// so existing sources.yml entries need no edit to opt in.
	SkipBodyExtraction bool `yaml:"skip_body_extraction"`
}

// Validate checks the structural minimum. name is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching Phase 5a's "unknown parser
// tolerated on a disabled site" precedent.
func (c WebsiteConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("websites: name is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return fmt.Errorf("websites[%s]: url is required", c.Name)
	}
	if c.Parser == "" {
		return fmt.Errorf("websites[%s]: parser is required", c.Name)
	}
	switch c.RenderMode {
	case "", "http", "browser", "auto":
	default:
		return fmt.Errorf("websites[%s]: render_mode must be 'http', 'browser' or 'auto', got %q", c.Name, c.RenderMode)
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("websites[%s]: interval_minutes must be positive", c.Name)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("websites[%s]: max_items_per_cycle must be positive", c.Name)
	}
	return nil
}

// RSSFeedConfig is one configured RSS/Atom feed, fetched directly (no
// bridge, no Scraping Service).
type RSSFeedConfig struct {
	Name             string   `yaml:"name"`
	URL              string   `yaml:"url"`
	SourceType       string   `yaml:"source_type"`
	IntervalMinutes  int      `yaml:"interval_minutes"`
	MaxItemsPerCycle int      `yaml:"max_items_per_cycle"`
	MaxAgeHours      int      `yaml:"max_age_hours"`
	BlockedKeywords  []string `yaml:"blocked_keywords"`
	RequiredKeywords []string `yaml:"required_keywords"`
	BlockedAuthors   []string `yaml:"blocked_authors"`
	Enabled          bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. name is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c RSSFeedConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("rss_feeds: name is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return fmt.Errorf("rss_feeds[%s]: url is required", c.Name)
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("rss_feeds[%s]: interval_minutes must be positive", c.Name)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("rss_feeds[%s]: max_items_per_cycle must be positive", c.Name)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("rss_feeds[%s]: max_age_hours must be positive", c.Name)
	}
	return nil
}

// RedditConfig lists the subreddit RSS feeds to fetch.
type RedditConfig struct {
	Feeds []RedditFeedConfig `yaml:"feeds"`
}

// RedditFeedConfig is one subreddit's public .rss feed. Mode is one of
// Reddit's listing modes; empty defaults to "new" at fetch time (see
// adapters/outbound/reddit).
type RedditFeedConfig struct {
	Subreddit        string `yaml:"subreddit"`
	Mode             string `yaml:"mode"`
	SourceType       string `yaml:"source_type"`
	IntervalMinutes  int    `yaml:"interval_minutes"`
	MaxItemsPerCycle int    `yaml:"max_items_per_cycle"`
	MaxAgeHours      int    `yaml:"max_age_hours"`
	Enabled          bool   `yaml:"enabled"`
}

// Validate checks the structural minimum. subreddit is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c RedditFeedConfig) Validate() error {
	if c.Subreddit == "" {
		return fmt.Errorf("reddit.feeds: subreddit is required")
	}
	if !c.Enabled {
		return nil
	}
	switch c.Mode {
	case "", "new", "hot", "top", "rising":
	default:
		return fmt.Errorf("reddit.feeds[%s]: mode must be 'new', 'hot', 'top' or 'rising', got %q", c.Subreddit, c.Mode)
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("reddit.feeds[%s]: interval_minutes must be positive", c.Subreddit)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("reddit.feeds[%s]: max_items_per_cycle must be positive", c.Subreddit)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("reddit.feeds[%s]: max_age_hours must be positive", c.Subreddit)
	}
	return nil
}

// BlueskyConfig lists the Bluesky accounts and curated feeds to fetch via
// the public AT Protocol API (no authentication).
type BlueskyConfig struct {
	Accounts []BlueskyAccountConfig `yaml:"accounts"`
	Feeds    []BlueskyFeedConfig    `yaml:"feeds"`
}

// BlueskyAccountConfig is one Bluesky account whose public author feed is
// fetched via app.bsky.feed.getAuthorFeed.
type BlueskyAccountConfig struct {
	Handle           string   `yaml:"handle"`
	Name             string   `yaml:"name"`
	SourceType       string   `yaml:"source_type"`
	IntervalMinutes  int      `yaml:"interval_minutes"`
	MaxItemsPerCycle int      `yaml:"max_items_per_cycle"`
	MaxAgeHours      int      `yaml:"max_age_hours"`
	BlockedURLs      []string `yaml:"blocked_urls"`
	BlockedKeywords  []string `yaml:"blocked_keywords"`
	SkipAuthors      []string `yaml:"skip_authors"`
	Enabled          bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. handle is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c BlueskyAccountConfig) Validate() error {
	if c.Handle == "" {
		return fmt.Errorf("bluesky.accounts: handle is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("bluesky.accounts[%s]: interval_minutes must be positive", c.Handle)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("bluesky.accounts[%s]: max_items_per_cycle must be positive", c.Handle)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("bluesky.accounts[%s]: max_age_hours must be positive", c.Handle)
	}
	return nil
}

// BlueskyFeedConfig is one curated Bluesky feed (an "at://...feed.generator"
// URI) fetched via app.bsky.feed.getFeed.
type BlueskyFeedConfig struct {
	URI              string   `yaml:"uri"`
	Name             string   `yaml:"name"`
	SourceType       string   `yaml:"source_type"`
	IntervalMinutes  int      `yaml:"interval_minutes"`
	MaxItemsPerCycle int      `yaml:"max_items_per_cycle"`
	MaxAgeHours      int      `yaml:"max_age_hours"`
	BlockedURLs      []string `yaml:"blocked_urls"`
	BlockedKeywords  []string `yaml:"blocked_keywords"`
	SkipAuthors      []string `yaml:"skip_authors"`
	Enabled          bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. uri is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c BlueskyFeedConfig) Validate() error {
	if c.URI == "" {
		return fmt.Errorf("bluesky.feeds: uri is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("bluesky.feeds[%s]: interval_minutes must be positive", c.URI)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("bluesky.feeds[%s]: max_items_per_cycle must be positive", c.URI)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("bluesky.feeds[%s]: max_age_hours must be positive", c.URI)
	}
	return nil
}

// TwitterConfig points at the local twitter_proxy RSS bridge (never the
// Scraping Service) and lists the accounts/hashtag searches
// to fetch through it.
type TwitterConfig struct {
	ProxyBaseURL      string                 `yaml:"proxy_base_url"`
	ProxyFallbackURLs []string               `yaml:"proxy_fallback_urls"`
	Accounts          []TwitterAccountConfig `yaml:"accounts"`
	Searches          []TwitterSearchConfig  `yaml:"searches"`
	Lists             []TwitterListConfig    `yaml:"lists"`
}

// Validate requires proxy_base_url only once at least one account,
// search, or list is configured; an entirely empty section is valid (the
// operator doesn't run the bridge).
func (c TwitterConfig) Validate() error {
	if len(c.Accounts)+len(c.Searches)+len(c.Lists) == 0 {
		return nil
	}
	if c.ProxyBaseURL == "" {
		return fmt.Errorf("twitter: proxy_base_url is required when accounts, searches, or lists are configured")
	}
	return nil
}

// TwitterAccountConfig is one Twitter/X account fetched via twitter_proxy's
// user-timeline RSS endpoint.
type TwitterAccountConfig struct {
	Handle           string   `yaml:"handle"`
	Name             string   `yaml:"name"`
	SourceType       string   `yaml:"source_type"`
	IntervalMinutes  int      `yaml:"interval_minutes"`
	MaxItemsPerCycle int      `yaml:"max_items_per_cycle"`
	MaxAgeHours      int      `yaml:"max_age_hours"`
	SkipAuthors      []string `yaml:"skip_authors"`
	BlockedURLs      []string `yaml:"blocked_urls"`
	BlockedKeywords  []string `yaml:"blocked_keywords"`
	Enabled          bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. handle is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c TwitterAccountConfig) Validate() error {
	if c.Handle == "" {
		return fmt.Errorf("twitter.accounts: handle is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("twitter.accounts[%s]: interval_minutes must be positive", c.Handle)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("twitter.accounts[%s]: max_items_per_cycle must be positive", c.Handle)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("twitter.accounts[%s]: max_age_hours must be positive", c.Handle)
	}
	return nil
}

// TwitterSearchConfig is one hashtag search fetched via twitter_proxy's
// search RSS endpoint.
type TwitterSearchConfig struct {
	Hashtag            string   `yaml:"hashtag"`
	Name               string   `yaml:"name"`
	SourceType         string   `yaml:"source_type"`
	IntervalMinutes    int      `yaml:"interval_minutes"`
	MaxItemsPerCycle   int      `yaml:"max_items_per_cycle"`
	MaxAgeHours        int      `yaml:"max_age_hours"`
	SkipAuthors        []string `yaml:"skip_authors"`
	SkipAuthorPrefixes []string `yaml:"skip_author_prefixes"`
	BlockedURLs        []string `yaml:"blocked_urls"`
	BlockedKeywords    []string `yaml:"blocked_keywords"`
	Enabled            bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. hashtag is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c TwitterSearchConfig) Validate() error {
	if c.Hashtag == "" {
		return fmt.Errorf("twitter.searches: hashtag is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("twitter.searches[%s]: interval_minutes must be positive", c.Hashtag)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("twitter.searches[%s]: max_items_per_cycle must be positive", c.Hashtag)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("twitter.searches[%s]: max_age_hours must be positive", c.Hashtag)
	}
	return nil
}

// TwitterListConfig is one Twitter/X list fetched via twitter_proxy's
// list-timeline RSS endpoint. Like a search, a list aggregates tweets from
// many authors, so it carries the same per-author filtering fields as
// TwitterSearchConfig.
type TwitterListConfig struct {
	ID                 string   `yaml:"id"`
	Name               string   `yaml:"name"`
	SourceType         string   `yaml:"source_type"`
	IntervalMinutes    int      `yaml:"interval_minutes"`
	MaxItemsPerCycle   int      `yaml:"max_items_per_cycle"`
	MaxAgeHours        int      `yaml:"max_age_hours"`
	SkipAuthors        []string `yaml:"skip_authors"`
	SkipAuthorPrefixes []string `yaml:"skip_author_prefixes"`
	BlockedURLs        []string `yaml:"blocked_urls"`
	BlockedKeywords    []string `yaml:"blocked_keywords"`
	Enabled            bool     `yaml:"enabled"`
}

// Validate checks the structural minimum. id is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c TwitterListConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("twitter.lists: id is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("twitter.lists[%s]: interval_minutes must be positive", c.ID)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("twitter.lists[%s]: max_items_per_cycle must be positive", c.ID)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("twitter.lists[%s]: max_age_hours must be positive", c.ID)
	}
	return nil
}

// InstagramConfig points at the local Instagram RSS bridge (never the
// Scraping Service) and lists the accounts to fetch through
// it.
type InstagramConfig struct {
	RSSBaseURL      string                   `yaml:"rss_base_url"`
	RSSPathTemplate string                   `yaml:"rss_path_template"`
	Accounts        []InstagramAccountConfig `yaml:"accounts"`
}

// Validate requires rss_base_url only once at least one account is
// configured; an entirely empty section is valid.
func (c InstagramConfig) Validate() error {
	if len(c.Accounts) == 0 {
		return nil
	}
	if c.RSSBaseURL == "" {
		return fmt.Errorf("instagram: rss_base_url is required when accounts are configured")
	}
	return nil
}

// InstagramAccountConfig is one Instagram account fetched via the RSS
// bridge's per-user endpoint.
type InstagramAccountConfig struct {
	Handle           string `yaml:"handle"`
	Name             string `yaml:"name"`
	SourceType       string `yaml:"source_type"`
	IntervalMinutes  int    `yaml:"interval_minutes"`
	MaxItemsPerCycle int    `yaml:"max_items_per_cycle"`
	MaxAgeHours      int    `yaml:"max_age_hours"`
	Enabled          bool   `yaml:"enabled"`
}

// Validate checks the structural minimum. handle is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c InstagramAccountConfig) Validate() error {
	if c.Handle == "" {
		return fmt.Errorf("instagram.accounts: handle is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("instagram.accounts[%s]: interval_minutes must be positive", c.Handle)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("instagram.accounts[%s]: max_items_per_cycle must be positive", c.Handle)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("instagram.accounts[%s]: max_age_hours must be positive", c.Handle)
	}
	return nil
}

// YouTubeConfig points at the local YouTube RSS bridge (never the Scraping
// Service) and lists the channels to fetch through it.
type YouTubeConfig struct {
	RSSBaseURL string                 `yaml:"rss_base_url"`
	Accounts   []YouTubeAccountConfig `yaml:"accounts"`
}

// Validate requires rss_base_url only once at least one account is
// configured; an entirely empty section is valid.
func (c YouTubeConfig) Validate() error {
	if len(c.Accounts) == 0 {
		return nil
	}
	if c.RSSBaseURL == "" {
		return fmt.Errorf("youtube: rss_base_url is required when accounts are configured")
	}
	return nil
}

// YouTubeAccountConfig is one YouTube channel fetched via the RSS bridge's
// per-channel endpoint.
type YouTubeAccountConfig struct {
	Name             string `yaml:"name"`
	ChannelID        string `yaml:"channel_id"`
	SourceType       string `yaml:"source_type"`
	IntervalMinutes  int    `yaml:"interval_minutes"`
	MaxItemsPerCycle int    `yaml:"max_items_per_cycle"`
	MaxAgeHours      int    `yaml:"max_age_hours"`
	Enabled          bool   `yaml:"enabled"`
}

// Validate checks the structural minimum. channel_id is required unconditionally
// (it is the identity/dedup key even for a disabled entry); every other
// field is only checked when Enabled, matching WebsiteConfig's convention.
func (c YouTubeAccountConfig) Validate() error {
	if c.ChannelID == "" {
		return fmt.Errorf("youtube.accounts: channel_id is required")
	}
	if !c.Enabled {
		return nil
	}
	if c.IntervalMinutes <= 0 {
		return fmt.Errorf("youtube.accounts[%s]: interval_minutes must be positive", c.ChannelID)
	}
	if c.MaxItemsPerCycle <= 0 {
		return fmt.Errorf("youtube.accounts[%s]: max_items_per_cycle must be positive", c.ChannelID)
	}
	if c.MaxAgeHours <= 0 {
		return fmt.Errorf("youtube.accounts[%s]: max_age_hours must be positive", c.ChannelID)
	}
	return nil
}

// Validate runs every section's per-entry Validate and rejects duplicate
// identity keys within a section.
func (s Sources) Validate() error {
	seenWebsites := make(map[string]bool, len(s.Websites))
	for _, w := range s.Websites {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
		if seenWebsites[w.Name] {
			return fmt.Errorf("sourcesfile: websites: duplicate name %q", w.Name)
		}
		seenWebsites[w.Name] = true
	}
	seenRSS := make(map[string]bool, len(s.RSSFeeds))
	for _, f := range s.RSSFeeds {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
		if seenRSS[f.Name] {
			return fmt.Errorf("sourcesfile: rss_feeds: duplicate name %q", f.Name)
		}
		seenRSS[f.Name] = true
	}
	for _, f := range s.Reddit.Feeds {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	for _, a := range s.Bluesky.Accounts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	for _, f := range s.Bluesky.Feeds {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	if err := s.Twitter.Validate(); err != nil {
		return fmt.Errorf("sourcesfile: %w", err)
	}
	for _, a := range s.Twitter.Accounts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	for _, srch := range s.Twitter.Searches {
		if err := srch.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	for _, l := range s.Twitter.Lists {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	if err := s.Instagram.Validate(); err != nil {
		return fmt.Errorf("sourcesfile: %w", err)
	}
	for _, a := range s.Instagram.Accounts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	if err := s.YouTube.Validate(); err != nil {
		return fmt.Errorf("sourcesfile: %w", err)
	}
	for _, a := range s.YouTube.Accounts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("sourcesfile: %w", err)
		}
	}
	ids, err := s.IDs()
	if err != nil {
		return fmt.Errorf("sourcesfile: %w", err)
	}
	seenIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seenIDs[id.String()] {
			return fmt.Errorf("sourcesfile: duplicate source id %q across sources.yml — every source must be globally unique", id.String())
		}
		seenIDs[id.String()] = true
	}
	return nil
}

// Load reads path, decodes it strictly (unknown keys rejected), validates
// the result, and returns it.
func Load(path string) (*Sources, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI/config value, not user input
	if err != nil {
		return nil, fmt.Errorf("sourcesfile: read %s: %w", path, err)
	}

	var s Sources
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("sourcesfile: parse %s: %w", path, err)
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
