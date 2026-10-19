package fetcherwiring

import (
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/compositefetcher"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/reddit"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/rss"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/youtube"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	ReasonDisabledFmt      = "config: fetch_sources.%s.enabled=false"
	ReasonNoEnabledEntries = "no enabled catalog entries"
)

// fetchHTTPTimeout/MaxRetries/BackoffBase/BackoffMax stay constants —
// only Timeout/ResponseLimitBytes are governed by cfg.HTTP (Task 8 wires
// these two into rssClientConfig below); the retry/backoff shape has no
// corresponding settings.yml keys, matching HTTPConfig's current fields.
const (
	fetchMaxRetries  = 2
	fetchBackoffBase = 500 * time.Millisecond
	fetchBackoffMax  = 5 * time.Second
)

// RSSClientConfig derives the shared RSS/HTTP client settings from cfg.
func RSSClientConfig(cfg *config.Config) rssclient.Config {
	return rssclient.Config{
		Timeout:            cfg.HTTP.Timeout(),
		MaxRetries:         fetchMaxRetries,
		BackoffBase:        fetchBackoffBase,
		BackoffMax:         fetchBackoffMax,
		ResponseLimitBytes: cfg.HTTP.MaxResponseBytes,
		AllowPrivateHosts:  cfg.HTTP.AllowPrivateHosts,
	}
}

func buildRSS(cfg *config.Config, catalog *sourcesfile.Sources, logger out.Logger, newScraper func(*config.Config, out.Logger) out.Scraper) ([]compositefetcher.Source, CapabilityEntry, bool) {
	entry := CapabilityEntry{Category: "rss"}
	if !cfg.FetchSources.RSS.Enabled {
		entry.Reason = fmt.Sprintf(ReasonDisabledFmt, "rss")
		return nil, entry, false
	}
	rssCfg := RSSClientConfig(cfg)
	var scraper out.Scraper
	if cfg.ScrapingService.Enabled && newScraper != nil {
		scraper = newScraper(cfg, logger)
	}
	var sources []compositefetcher.Source
	for _, f := range catalog.RSSFeeds {
		if !f.Enabled {
			continue
		}
		feedFetcher := rss.NewFetcherWithScraper([]rss.FeedConfig{{
			Name: f.Name, URL: f.URL, SourceType: f.SourceType,
			MaxAge:          time.Duration(f.MaxAgeHours) * time.Hour,
			BlockedKeywords: f.BlockedKeywords, RequiredKeywords: f.RequiredKeywords, BlockedAuthors: f.BlockedAuthors,
		}}, rssCfg, scraper, logger)
		sources = append(sources, compositefetcher.Source{
			Name: "rss:" + f.Name, Fetcher: feedFetcher,
			Interval: time.Duration(f.IntervalMinutes) * time.Minute, MaxItemsPerCycle: f.MaxItemsPerCycle,
		})
	}
	if len(sources) == 0 {
		entry.Reason = ReasonNoEnabledEntries
		return nil, entry, false
	}
	entry.Active = true
	entry.Reason = "active"
	return sources, entry, false
}

func buildReddit(cfg *config.Config, catalog *sourcesfile.Sources, logger out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
	entry := CapabilityEntry{Category: "reddit"}
	if !cfg.FetchSources.Reddit.Enabled {
		entry.Reason = fmt.Sprintf(ReasonDisabledFmt, "reddit")
		return nil, entry, false
	}
	rssCfg := RSSClientConfig(cfg)
	var sources []compositefetcher.Source
	for _, f := range catalog.Reddit.Feeds {
		if !f.Enabled {
			continue
		}
		feedFetcher := reddit.NewFetcher([]reddit.FeedConfig{{
			Subreddit: f.Subreddit, Mode: f.Mode, SourceType: f.SourceType,
			MaxAge: time.Duration(f.MaxAgeHours) * time.Hour,
		}}, rssCfg, logger)
		sources = append(sources, compositefetcher.Source{
			Name: "reddit:" + f.Subreddit, Fetcher: feedFetcher,
			Interval: time.Duration(f.IntervalMinutes) * time.Minute, MaxItemsPerCycle: f.MaxItemsPerCycle,
		})
	}
	if len(sources) == 0 {
		entry.Reason = ReasonNoEnabledEntries
		return nil, entry, false
	}
	entry.Active = true
	entry.Reason = "active"
	return sources, entry, false
}

func buildBluesky(cfg *config.Config, catalog *sourcesfile.Sources, logger out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
	entry := CapabilityEntry{Category: "bluesky"}
	if !cfg.FetchSources.Bluesky.Enabled {
		entry.Reason = fmt.Sprintf(ReasonDisabledFmt, "bluesky")
		return nil, entry, false
	}
	client := bluesky.NewClient(cfg.HTTP.Timeout(), cfg.HTTP.MaxResponseBytes)
	var sources []compositefetcher.Source
	for _, a := range catalog.Bluesky.Accounts {
		if !a.Enabled {
			continue
		}
		acctFetcher := bluesky.NewFetcher([]bluesky.AccountConfig{{
			Handle: a.Handle, Name: a.Name, SourceType: a.SourceType,
			MaxAge:      time.Duration(a.MaxAgeHours) * time.Hour,
			BlockedURLs: a.BlockedURLs, BlockedKeywords: a.BlockedKeywords, KeepKeywords: a.KeepKeywords, SkipAuthors: a.SkipAuthors,
		}}, nil, client, logger)
		sources = append(sources, compositefetcher.Source{
			Name: "bluesky-account:" + a.Handle, Fetcher: acctFetcher,
			Interval: time.Duration(a.IntervalMinutes) * time.Minute, MaxItemsPerCycle: a.MaxItemsPerCycle,
		})
	}
	for _, f := range catalog.Bluesky.Feeds {
		if !f.Enabled {
			continue
		}
		feedFetcher := bluesky.NewFetcher(nil, []bluesky.FeedConfig{{
			URI: f.URI, Name: f.Name, SourceType: f.SourceType,
			MaxAge:      time.Duration(f.MaxAgeHours) * time.Hour,
			BlockedURLs: f.BlockedURLs, BlockedKeywords: f.BlockedKeywords, KeepKeywords: f.KeepKeywords, SkipAuthors: f.SkipAuthors,
		}}, client, logger)
		sources = append(sources, compositefetcher.Source{
			Name: "bluesky-feed:" + f.URI, Fetcher: feedFetcher,
			Interval: time.Duration(f.IntervalMinutes) * time.Minute, MaxItemsPerCycle: f.MaxItemsPerCycle,
		})
	}
	if len(sources) == 0 {
		entry.Reason = ReasonNoEnabledEntries
		return nil, entry, false
	}
	entry.Active = true
	entry.Reason = "active"
	return sources, entry, false
}

func buildYouTube(cfg *config.Config, catalog *sourcesfile.Sources, profile config.Profile, logger out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
	entry := CapabilityEntry{Category: "youtube"}
	if !cfg.FetchSources.YouTube.Enabled {
		entry.Reason = fmt.Sprintf(ReasonDisabledFmt, "youtube")
		return nil, entry, false
	}
	if reason, bad := invalidBridgeHost(catalog.YouTube.RSSBaseURL, profile); bad {
		entry.Reason = reason
		return nil, entry, true
	}
	rssCfg := RSSClientConfig(cfg)
	// The bridge base URL is operator infrastructure that normally runs
	// beside the bot (see invalidBridgeHost), so private hosts are allowed;
	// netguard still refuses link-local and cloud metadata addresses.
	rssCfg.AllowPrivateHosts = true
	var sources []compositefetcher.Source
	for _, a := range catalog.YouTube.Accounts {
		if !a.Enabled {
			continue
		}
		if profile == config.ProfileProduction {
			if reason, bad := invalidYouTubeChannelID(a.ChannelID); bad {
				logger.Warn("skipping youtube account with placeholder channel_id in production", "name", a.Name, "reason", reason)
				continue
			}
		}
		acctFetcher := youtube.NewFetcher(catalog.YouTube.RSSBaseURL, []youtube.AccountConfig{{
			Name: a.Name, ChannelID: a.ChannelID, SourceType: a.SourceType,
			MaxAge: time.Duration(a.MaxAgeHours) * time.Hour,
		}}, rssCfg, logger)
		sources = append(sources, compositefetcher.Source{
			Name: "youtube:" + a.ChannelID, Fetcher: acctFetcher,
			Interval: time.Duration(a.IntervalMinutes) * time.Minute, MaxItemsPerCycle: a.MaxItemsPerCycle,
		})
	}
	if len(sources) == 0 {
		entry.Reason = ReasonNoEnabledEntries
		return nil, entry, false
	}
	entry.Active = true
	entry.Reason = "active"
	return sources, entry, false
}

// invalidBridgeHost reports whether baseURL cannot be used as a bridge
// endpoint: empty and malformed URLs are bad, but loopback hosts are valid
// because same-host production deployments run the bridges
// beside phins-news-engine. No network call: only url.Parse. Bridge URLs live
// in sources.yml, not Config, which is why this check lives here rather
// than in Config.ValidateForProfile.
func invalidBridgeHost(baseURL string, profile config.Profile) (reason string, bad bool) {
	if baseURL == "" {
		return "bridge endpoint not configured", true
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "bridge endpoint is not a valid URL", true
	}
	return "", false
}

var placeholderYouTubeChannelID = regexp.MustCompile(`(?i)^UC[x]+$`)

// invalidYouTubeChannelID reports whether channelID is the known
// committed placeholder ("UCxxxxxxxx"), matches the generic UC-followed-
// by-only-x's placeholder shape, or is empty.
func invalidYouTubeChannelID(channelID string) (reason string, bad bool) {
	if channelID == "" {
		return "channel_id is empty", true
	}
	if placeholderYouTubeChannelID.MatchString(channelID) {
		return "channel_id is a known placeholder", true
	}
	return "", false
}
