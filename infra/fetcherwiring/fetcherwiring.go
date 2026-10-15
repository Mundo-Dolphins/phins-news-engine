// Package fetcherwiring builds the process's out.Fetcher from the loaded
// configuration and content-source catalog, evaluating each of the 7
// content-fetching categories (website, rss, reddit, bluesky, twitter,
// instagram, youtube) independently.
// No function in this package makes a network call.
package fetcherwiring

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/compositefetcher"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ErrorPolicy resolves what happens when an enabled category is missing
// its real dependency.
type ErrorPolicy string

const (
	PolicyFatal   ErrorPolicy = "fatal"
	PolicyDegrade ErrorPolicy = "degrade"
)

// CapabilityEntry reports one category's active/inactive status and a
// sanitized reason — never a secret or a raw credentialed URL.
type CapabilityEntry struct {
	Category string
	Active   bool
	Reason   string
}

// Report is every category's CapabilityEntry after one Build call.
type Report struct {
	Entries []CapabilityEntry
}

// Active returns the category names that are active.
func (r Report) Active() []string {
	var names []string
	for _, e := range r.Entries {
		if e.Active {
			names = append(names, e.Category)
		}
	}
	return names
}

// Inactive returns every inactive category name mapped to its reason.
func (r Report) Inactive() map[string]string {
	m := make(map[string]string)
	for _, e := range r.Entries {
		if !e.Active {
			m[e.Category] = e.Reason
		}
	}
	return m
}

// resolvePolicy applies a category's optional on_missing_dependency
// override, falling back to the active profile's default: local degrades,
// test and production are fatal.
func resolvePolicy(profile config.Profile, override string) ErrorPolicy {
	switch override {
	case "fatal":
		return PolicyFatal
	case "degrade":
		return PolicyDegrade
	}
	if profile == config.ProfileLocal {
		return PolicyDegrade
	}
	return PolicyFatal
}

// CategoryBuilder builds the sources of one fetch category.
// depMissing=true means a required dependency is absent (policy decides if fatal).
type CategoryBuilder func(cfg *config.Config, catalog *sourcesfile.Sources, profile config.Profile, logger out.Logger) (sources []compositefetcher.Source, entry CapabilityEntry, depMissing bool)

// Extensions lets a host program supply builders for categories the library does not implement.
type Extensions struct {
	Categories map[string]CategoryBuilder // key = category name: "website","twitter","instagram"
	// Scraper optionally builds the out.Scraper used as the RSS fallback; nil disables it.
	Scraper func(cfg *config.Config, logger out.Logger) out.Scraper
}

// Order is the fixed order in which categories are evaluated and reported.
var Order = []string{"website", "rss", "reddit", "bluesky", "twitter", "instagram", "youtube"}

// Build evaluates all 7 categories independently and returns the
// composed out.Fetcher plus a capability report. The returned error is
// non-nil only for a fatal condition: profile-level credential/placeholder
// validation, a category's on_missing_dependency: fatal (or fatal-by-
// profile-default) resolution, or zero active sources overall — the last
// applies regardless of profile and has no override.
func Build(cfg *config.Config, catalog *sourcesfile.Sources, profile config.Profile, clock out.Clock, logger out.Logger, ext Extensions) (out.Fetcher, Report, error) {
	if err := cfg.ValidateForProfile(profile); err != nil {
		return nil, Report{}, err
	}

	known := make(map[string]bool, len(Order))
	for _, n := range Order {
		known[n] = true
	}
	var unknown []string
	for k := range ext.Categories {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, Report{}, fmt.Errorf("fetcherwiring: unknown extension category %q (valid: %s)", unknown[0], strings.Join(Order, ", "))
	}

	var report Report
	var allSources []compositefetcher.Source

	builtin := map[string]CategoryBuilder{
		"rss": func(c *config.Config, cat *sourcesfile.Sources, _ config.Profile, l out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
			return buildRSS(c, cat, l, ext.Scraper)
		},
		"reddit": func(c *config.Config, cat *sourcesfile.Sources, _ config.Profile, l out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
			return buildReddit(c, cat, l)
		},
		"bluesky": func(c *config.Config, cat *sourcesfile.Sources, _ config.Profile, l out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
			return buildBluesky(c, cat, l)
		},
		"youtube": buildYouTube,
	}
	cats := map[string]config.SourceCategoryConfig{
		"website": cfg.FetchSources.Website, "rss": cfg.FetchSources.RSS, "reddit": cfg.FetchSources.Reddit,
		"bluesky": cfg.FetchSources.Bluesky, "twitter": cfg.FetchSources.Twitter,
		"instagram": cfg.FetchSources.Instagram, "youtube": cfg.FetchSources.YouTube,
	}
	for _, name := range Order {
		cat := cats[name]
		b, ok := builtin[name]
		if !ok {
			b, ok = ext.Categories[name]
			ok = ok && b != nil
		}
		if !ok {
			entry := CapabilityEntry{Category: name}
			if cat.Enabled {
				entry.Reason = "no builder registered for this category"
				if resolvePolicy(profile, cat.OnMissingDependency) == PolicyFatal {
					return nil, Report{}, fmt.Errorf("fetcherwiring: %s: %s", name, entry.Reason)
				}
			} else {
				entry.Reason = fmt.Sprintf(ReasonDisabledFmt, name)
			}
			report.Entries = append(report.Entries, entry)
			continue
		}
		sources, entry, depMissing := b(cfg, catalog, profile, logger)
		if depMissing && resolvePolicy(profile, cat.OnMissingDependency) == PolicyFatal {
			return nil, Report{}, fmt.Errorf("fetcherwiring: %s: %s", entry.Category, entry.Reason)
		}
		report.Entries = append(report.Entries, entry)
		allSources = append(allSources, sources...)
	}

	if len(allSources) == 0 {
		return nil, report, fmt.Errorf("fetcherwiring: no active fetch sources — check fetch_sources in settings.yml and the enabled entries in sources.yml")
	}

	return compositefetcher.New(allSources, cfg.Fetch.GlobalConcurrency, cfg.Fetch.FanInBuffer, clock, logger), report, nil
}
