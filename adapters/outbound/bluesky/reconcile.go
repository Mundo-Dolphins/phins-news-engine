package bluesky

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/outboxreconcile"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// reconcileFeedLimit is the most posts getAuthorFeed returns per call.
const reconcileFeedLimit = 100

// FindSent implements out.DestinationReconciler: it decides whether a send
// that ended in an AmbiguousError produced a post, by looking for the account's
// own post with the exact text the send would have had and a createdAt no
// earlier than lookup.SentAt minus the clock skew (ADR 0030, ADR 0035).
//
// Only the newest page of the author feed is read, so a busy account can push
// the send out of reach; the result is then not covered, which the caller
// treats as inconclusive rather than as "not sent".
func (p *Publisher) FindSent(ctx context.Context, lookup appout.SentLookup) (string, bool, bool, error) {
	want, err := postText(lookup.Article, lookup.Payload)
	if err != nil {
		return "", false, false, fmt.Errorf("bluesky: reconcile: %w", err)
	}
	feed, err := p.feed.GetAuthorFeed(ctx, p.cfg.Handle, reconcileFeedLimit)
	if err != nil {
		return "", false, false, fmt.Errorf("bluesky: reconcile: %w", err)
	}

	posts := make([]outboxreconcile.Post, 0, len(feed))
	for _, item := range feed {
		// The author feed also lists the account's reposts of other people.
		if h := item.Post.Author.Handle; h != "" && !strings.EqualFold(h, p.cfg.Handle) {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, item.Post.Record.CreatedAt)
		if err != nil {
			continue
		}
		posts = append(posts, outboxreconcile.Post{ID: item.Post.URI, Text: item.Post.Record.Text, PostedAt: created})
	}
	id, matched, covered := outboxreconcile.MatchPost(posts, want, lookup.SentAt, lookup.ClockSkew)
	return id, matched, covered, nil
}
