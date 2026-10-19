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
	// ClusterID is the cluster the article belongs to ("" when it has none).
	ClusterID string
}

// RecentStoryRepository lists what the channel already carries, for the
// story-repeat guard.
type RecentStoryRepository interface {
	// ListRecentlyPublishable returns articles fetched at or after since
	// whose status is published, or queued for publication
	// (pending_publication, publishing, pending_retry), newest first,
	// excluding excludeID.
	ListRecentlyPublishable(ctx context.Context, since time.Time, excludeID string, limit int) ([]RecentStory, error)
	// ListRecentClustered returns articles fetched at or after since that
	// belong to an open cluster, whatever their publication outcome (a
	// retained or rejected-quality member is still part of the story), newest
	// first, excluding excludeID.
	ListRecentClustered(ctx context.Context, since time.Time, excludeID string, limit int) ([]RecentStory, error)
}
