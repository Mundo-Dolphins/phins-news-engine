package sourcesfile_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

const defaultBlockedURLsFixture = `
default_blocked_urls:
  - "cheapjersey"
  - "pickjersey"
rss_feeds:
  - name: "Example Isotopes Feed"
    url: "https://example.com/feed.xml"
    source_type: hard_news
    interval_minutes: 5
    max_items_per_cycle: 20
    max_age_hours: 24
    blocked_keywords: ["injury report"]
    enabled: true
bluesky:
  accounts:
    - handle: "isotopes.bsky.social"
      name: "Springfield Isotopes"
      source_type: social
      interval_minutes: 5
      max_items_per_cycle: 10
      max_age_hours: 24
      blocked_urls: ["https://youcheapjerseys.com/"]
      enabled: true
  feeds:
    - uri: "at://did:plc:example/app.bsky.feed.generator/example"
      name: "Isotopes News Feed"
      source_type: social
      interval_minutes: 5
      max_items_per_cycle: 15
      max_age_hours: 24
      blocked_urls: ["cheapjersey", "klipy.com"]
      enabled: true
twitter:
  proxy_base_url: "http://127.0.0.1:8090"
  accounts:
    - handle: "SprIsotopes"
      name: "Springfield Isotopes"
      source_type: social
      interval_minutes: 3
      max_items_per_cycle: 10
      max_age_hours: 24
      enabled: true
  searches:
    - hashtag: "Dolphins"
      name: "Dolphins Community"
      source_type: social
      interval_minutes: 3
      max_items_per_cycle: 15
      max_age_hours: 24
      blocked_urls: ["foco.com"]
      enabled: true
  lists:
    - id: "1234"
      name: "Isotopes List"
      source_type: social
      interval_minutes: 3
      max_items_per_cycle: 15
      max_age_hours: 24
      enabled: true
`

func TestLoad_MergesDefaultBlockedURLs(t *testing.T) {
	s, err := sourcesfile.Load(writeFixture(t, defaultBlockedURLsFixture))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// defaults first, per-source entries after, duplicates dropped.
	if want := []string{"cheapjersey", "pickjersey", "injury report"}; !reflect.DeepEqual(s.RSSFeeds[0].BlockedKeywords, want) {
		t.Errorf("RSSFeeds[0].BlockedKeywords = %v, want %v", s.RSSFeeds[0].BlockedKeywords, want)
	}
	// "https://youcheapjerseys.com/" is not a textual duplicate of the
	// default, so both stay; the matcher normalizes the scheme at match
	// time, and keeping the per-source entry preserves the operator's
	// intent for any future host-level matcher.
	if want := []string{"cheapjersey", "pickjersey", "https://youcheapjerseys.com/"}; !reflect.DeepEqual(s.Bluesky.Accounts[0].BlockedURLs, want) {
		t.Errorf("Bluesky.Accounts[0].BlockedURLs = %v, want %v", s.Bluesky.Accounts[0].BlockedURLs, want)
	}
	if want := []string{"cheapjersey", "pickjersey", "klipy.com"}; !reflect.DeepEqual(s.Bluesky.Feeds[0].BlockedURLs, want) {
		t.Errorf("Bluesky.Feeds[0].BlockedURLs = %v, want %v (per-source duplicate of a default must be dropped)", s.Bluesky.Feeds[0].BlockedURLs, want)
	}
	if want := []string{"cheapjersey", "pickjersey"}; !reflect.DeepEqual(s.Twitter.Accounts[0].BlockedURLs, want) {
		t.Errorf("Twitter.Accounts[0].BlockedURLs = %v, want %v", s.Twitter.Accounts[0].BlockedURLs, want)
	}
	if want := []string{"cheapjersey", "pickjersey", "foco.com"}; !reflect.DeepEqual(s.Twitter.Searches[0].BlockedURLs, want) {
		t.Errorf("Twitter.Searches[0].BlockedURLs = %v, want %v", s.Twitter.Searches[0].BlockedURLs, want)
	}
	if want := []string{"cheapjersey", "pickjersey"}; !reflect.DeepEqual(s.Twitter.Lists[0].BlockedURLs, want) {
		t.Errorf("Twitter.Lists[0].BlockedURLs = %v, want %v", s.Twitter.Lists[0].BlockedURLs, want)
	}
}

func TestLoad_NoDefaultsLeavesListsUntouched(t *testing.T) {
	const yamlContent = `
bluesky:
  feeds:
    - uri: "at://did:plc:example/app.bsky.feed.generator/example"
      name: "Isotopes News Feed"
      source_type: social
      interval_minutes: 5
      max_items_per_cycle: 15
      max_age_hours: 24
      blocked_urls: ["klipy.com"]
      enabled: true
`
	s, err := sourcesfile.Load(writeFixture(t, yamlContent))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := s.Bluesky.Feeds[0].BlockedURLs; !reflect.DeepEqual(got, []string{"klipy.com"}) {
		t.Errorf("BlockedURLs = %v, want the entry's own list unchanged without defaults", got)
	}
}

func TestLoad_MergeTrimsAndDropsBlanks(t *testing.T) {
	const yamlContent = `
default_blocked_urls:
  - "  cheapjersey  "
  - " "
  - "CHEAPJERSEY"
bluesky:
  feeds:
    - uri: "at://did:plc:example/app.bsky.feed.generator/example"
      name: "Isotopes News Feed"
      source_type: social
      interval_minutes: 5
      max_items_per_cycle: 15
      max_age_hours: 24
      blocked_urls: []
      enabled: true
`
	s, err := sourcesfile.Load(writeFixture(t, yamlContent))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// The trimmed entry is kept, the blank dropped, and the
	// case-variant duplicate dropped.
	if got := s.Bluesky.Feeds[0].BlockedURLs; !reflect.DeepEqual(got, []string{"  cheapjersey  "}) {
		t.Errorf("BlockedURLs = %v, want [\"  cheapjersey  \"]", got)
	}
}
