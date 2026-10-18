package out

import (
	"context"
	"time"
)

// RecentStory is an article that reached the channel or is queued to.
type RecentStory struct {
	ArticleID string
	Title     string
	SourceID  string
}

// RecentStoryRepository lists what the channel already carries, for the
// story-repeat guard.
type RecentStoryRepository interface {
	// ListRecentlyPublishable returns articles fetched at or after since
	// whose status is published, or queued for publication
	// (pending_publication, publishing, pending_retry), newest first,
	// excluding excludeID.
	ListRecentlyPublishable(ctx context.Context, since time.Time, excludeID string, limit int) ([]RecentStory, error)
}
