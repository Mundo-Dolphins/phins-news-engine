package source

// id.go implements the globally-unique, platform-namespaced source_id
// contract.
// Every producer of a source.Source.ID — every adapter, sourcesfile,
// sourcepolicyfile — must build IDs exclusively through NewID/ParseID;
// there is no other sanctioned way to construct one, which is what makes a
// Twitter/Instagram bare-handle collision structurally impossible going forward.

import (
	"fmt"
	"regexp"
	"strings"
)

// ID is a fully-qualified, validated source identifier: "platform:identifier".
// Immutable; the zero value is invalid (Platform() == "" fails Type.Valid()).
type ID struct {
	platform   Type
	identifier string
}

// platformRule is one platform's identifier normalization + validation.
type platformRule struct {
	// normalize lowercases/trims/strips as appropriate for this platform.
	// It runs before format is checked.
	normalize func(raw string) string
	// format lists every shape this platform's identifier may take; raw
	// (post-normalize) must match at least one.
	format []*regexp.Regexp
}

var (
	handleFormat  = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)
	hashtagFormat = regexp.MustCompile(`^hashtag-[a-z0-9_]+$`)
	listFormat    = regexp.MustCompile(`^list-[a-z0-9_]+$`)
	igFormat      = regexp.MustCompile(`^[a-z0-9._]{1,30}$`)
	bskyFormat    = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	bskyFeedFmt   = regexp.MustCompile(`^feed-[a-z0-9-]+$`)
	ytFormat      = regexp.MustCompile(`^UC[A-Za-z0-9_-]+$`)
	redditFormat  = regexp.MustCompile(`^reddit-[a-z0-9_]+$`)
	slugFormat    = regexp.MustCompile(`^[a-z0-9-]+$`)
)

func stripAtLowerTrim(raw string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@")))
}

var platformRules = map[Type]platformRule{
	TypeTwitter: {
		normalize: stripAtLowerTrim,
		format:    []*regexp.Regexp{handleFormat, hashtagFormat, listFormat},
	},
	TypeInstagram: {
		normalize: stripAtLowerTrim,
		format:    []*regexp.Regexp{igFormat},
	},
	TypeBluesky: {
		normalize: stripAtLowerTrim,
		format:    []*regexp.Regexp{bskyFormat, bskyFeedFmt},
	},
	TypeYouTube: {
		// Channel IDs are case-sensitive: trim only, never lowercase.
		normalize: strings.TrimSpace,
		format:    []*regexp.Regexp{ytFormat},
	},
	TypeReddit: {
		normalize: strings.ToLower,
		format:    []*regexp.Regexp{redditFormat},
	},
	TypeRSS: {
		normalize: Slug,
		format:    []*regexp.Regexp{slugFormat},
	},
	TypeWeb: {
		normalize: Slug,
		format:    []*regexp.Regexp{slugFormat},
	},
}

// NewID normalizes rawIdentifier per platform's rule and validates the
// result, returning an error for an unrecognized platform or an
// identifier that fails every one of that platform's allowed shapes.
// This is the single normalization path every adapter and config loader
// in the codebase must share (see the package doc comment).
func NewID(platform Type, rawIdentifier string) (ID, error) {
	rule, ok := platformRules[platform]
	if !ok {
		return ID{}, fmt.Errorf("source: unknown platform %q", platform)
	}
	normalized := rule.normalize(rawIdentifier)
	if normalized == "" {
		return ID{}, fmt.Errorf("source: empty identifier for platform %q", platform)
	}
	for _, re := range rule.format {
		if re.MatchString(normalized) {
			return ID{platform: platform, identifier: normalized}, nil
		}
	}
	return ID{}, fmt.Errorf("source: identifier %q is not a valid %s identifier", rawIdentifier, platform)
}

// ParseID splits raw on the first ':' into platform:identifier and
// validates both halves exactly as NewID does — parsing an
// already-declared ID applies the same format strictness regardless of
// where raw came from (DB row, source-policies.yml source_id value).
func ParseID(raw string) (ID, error) {
	i := strings.IndexByte(raw, ':')
	if i <= 0 || i == len(raw)-1 {
		return ID{}, fmt.Errorf("source: %q is not a valid platform:identifier source_id", raw)
	}
	platform := Type(raw[:i])
	identifier := raw[i+1:]
	rule, ok := platformRules[platform]
	if !ok {
		return ID{}, fmt.Errorf("source: %q: unknown platform %q", raw, platform)
	}
	for _, re := range rule.format {
		if re.MatchString(identifier) {
			return ID{platform: platform, identifier: identifier}, nil
		}
	}
	return ID{}, fmt.Errorf("source: %q: identifier %q is not a valid %s identifier", raw, identifier, platform)
}

// Platform returns id's platform.
func (id ID) Platform() Type { return id.platform }

// String returns the canonical "platform:identifier" form.
func (id ID) String() string { return string(id.platform) + ":" + id.identifier }

// MarshalText implements encoding.TextMarshaler so ID round-trips
// through gopkg.in/yaml.v3 (which falls back to TextMarshaler/
// TextUnmarshaler for scalar nodes) and encoding/json.
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ID) UnmarshalText(text []byte) error {
	parsed, err := ParseID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Slug lowercases name, collapses runs of whitespace/non-alphanumerics to
// a single '-', and trims leading/trailing '-'. Shared by every
// slug-based platform (RSS, Website) and by any caller that needs to
// build a slug-shaped raw identifier (e.g. a Bluesky custom feed's
// "feed-<slug>") before passing it to NewID.
func Slug(name string) string {
	var b strings.Builder
	lastDash := true // treat start-of-string as "just wrote a dash" so we never lead with one
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
