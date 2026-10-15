// Package normalization provides pure, deterministic URL and text
// normalization shared by exact and near-duplicate detection. It has no
// dependency on storage, HTTP, or config — callers build a URLConfig /
// TextConfig from their own configuration and pass plain values in.
package normalization

import (
	"fmt"
	"net/url"
	"strings"
)

// KnownLinkResolver rewrites raw into a canonical form if it recognizes the
// URL, returning the rewritten form and true. It returns raw unchanged and
// false otherwise. Resolvers run before generic normalization, in the order
// given to NormalizeURL; the first one that recognizes the URL wins.
type KnownLinkResolver func(raw string) (string, bool)

// URLConfig configures NormalizeURL.
type URLConfig struct {
	// TrackingParams lists query parameter names removed from every host.
	TrackingParams []string
	// DomainIgnoredParams lists additional query parameter names removed
	// only when the URL's host matches the map key exactly.
	DomainIgnoredParams map[string][]string
	// StripDefaultPorts removes ":80" on http and ":443" on https.
	StripDefaultPorts bool
	// TrailingSlash is "strip" or "keep". Never applied to the root path "/".
	TrailingSlash string
}

// Resolvers is the registry of known-link resolvers selectable by name from
// configuration (see infrastructure/config.URLNormalizationConfig).
var Resolvers = map[string]KnownLinkResolver{
	"youtube": YouTubeResolver,
}

// NormalizeURL canonicalizes raw: runs the first matching resolver (if
// any), lowercases scheme and host, drops the fragment, strips default
// ports, applies the trailing-slash policy, and removes only the
// explicitly configured tracking query parameters — an unknown parameter
// is never removed, which is the conservative default protecting real
// identifiers (not just tracking noise) from being treated as equivalent.
func NormalizeURL(raw string, cfg URLConfig, resolvers []KnownLinkResolver) (string, error) {
	for _, resolve := range resolvers {
		if rewritten, ok := resolve(raw); ok {
			raw = rewritten
			break
		}
	}

	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("normalization: parse url %q: %w", raw, err)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""

	if cfg.StripDefaultPorts {
		u.Host = stripDefaultPort(u.Scheme, u.Host)
	}

	if u.RawQuery != "" {
		ignored := make(map[string]bool, len(cfg.TrackingParams))
		for _, p := range cfg.TrackingParams {
			ignored[p] = true
		}
		for _, p := range cfg.DomainIgnoredParams[u.Host] {
			ignored[p] = true
		}
		q := u.Query()
		for p := range q {
			if ignored[p] {
				q.Del(p)
			}
		}
		u.RawQuery = q.Encode()
	}

	if cfg.TrailingSlash == "strip" && u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}

	return u.String(), nil
}

func stripDefaultPort(scheme, host string) string {
	switch {
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		return strings.TrimSuffix(host, ":80")
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		return strings.TrimSuffix(host, ":443")
	default:
		return host
	}
}

// YouTubeResolver rewrites youtube.com/youtu.be watch, shorts, live, and
// embed URLs to the canonical https://www.youtube.com/watch?v=ID form,
// dropping any other query parameters (e.g. playlist position, share
// tracking) since the video ID alone is the article's real identity.
func YouTubeResolver(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return raw, false
	}

	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	var videoID string
	switch host {
	case "youtu.be":
		videoID = strings.Trim(u.Path, "/")
	case "youtube.com", "m.youtube.com", "youtube-nocookie.com":
		switch {
		case u.Path == "/watch":
			videoID = strings.TrimSpace(u.Query().Get("v"))
		case strings.HasPrefix(u.Path, "/shorts/"):
			videoID = strings.TrimPrefix(u.Path, "/shorts/")
		case strings.HasPrefix(u.Path, "/live/"):
			videoID = strings.TrimPrefix(u.Path, "/live/")
		case strings.HasPrefix(u.Path, "/embed/"):
			videoID = strings.TrimPrefix(u.Path, "/embed/")
		}
	default:
		return raw, false
	}

	videoID = strings.Trim(videoID, "/")
	if videoID == "" {
		return raw, false
	}
	return "https://www.youtube.com/watch?v=" + videoID, true
}
