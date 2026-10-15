// Package source models the origin a piece of content was fetched from.
package source

import (
	"fmt"
	"time"
)

// Type identifies the kind of channel a Source publishes through.
type Type string

const (
	TypeRSS       Type = "rss"
	TypeWeb       Type = "web"
	TypeTwitter   Type = "twitter"
	TypeBluesky   Type = "bluesky"
	TypeInstagram Type = "instagram"
	TypeReddit    Type = "reddit"
	TypeYouTube   Type = "youtube"
	TypeOther     Type = "other"
)

// Valid reports whether t is one of the known source types.
func (t Type) Valid() bool {
	switch t {
	case TypeRSS, TypeWeb, TypeTwitter, TypeBluesky, TypeInstagram, TypeReddit, TypeYouTube, TypeOther:
		return true
	default:
		return false
	}
}

// Source is a configured content origin (a feed, a profile, a site).
type Source struct {
	ID          ID
	Name        string
	Type        Type
	Domain      string
	URL         string
	FirstSeenAt time.Time
}

// Validate checks that the source has the minimum data required to be used.
func (s Source) Validate() error {
	if s.ID == (ID{}) {
		return fmt.Errorf("source: id is required")
	}
	if s.Name == "" {
		return fmt.Errorf("source: name is required")
	}
	if !s.Type.Valid() {
		return fmt.Errorf("source: invalid type %q", s.Type)
	}
	return nil
}
