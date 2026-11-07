package bluesky

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/outboxreconcile"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// reconcileFeedLimit is the most posts getAuthorFeed returns per call.
const reconcileFeedLimit = 100

// FindSent implements out.DestinationReconciler: it decides whether a send
// that ended in an AmbiguousError produced a post, by looking for the account's
// own post with the exact text the send would have had and a createdAt no
// earlier than lookup.SentAt minus the clock skew (ADR 0030, ADR 0035). The
// returned ref carries the post's URI and CID.
//
// For a thread with lookup.ThreadProgress already posted, the post looked
// for is part len(ThreadProgress), and it must also reply to the last posted
// part; replies are then included in the feed read.
//
// Only the newest page of the author feed is read, so a busy account can push
// the send out of reach; the result is then not covered, which the caller
// treats as inconclusive rather than as "not sent".
func (p *Publisher) FindSent(ctx context.Context, lookup appout.SentLookup) (publishing.PostRef, bool, bool, error) {
	texts, err := threadTexts(lookup.Article, lookup.Payload)
	if err != nil {
		return publishing.PostRef{}, false, false, fmt.Errorf("bluesky: reconcile: %w", err)
	}
	part := len(lookup.ThreadProgress)
	if part >= len(texts) {
		return publishing.PostRef{}, false, false, fmt.Errorf("bluesky: reconcile: %d thread parts already posted, the thread has %d", part, len(texts))
	}
	want := texts[part]

	getFeed := p.feed.GetAuthorFeed
	var parent string
	if part > 0 {
		getFeed = p.feed.GetAuthorFeedWithReplies
		parent = lookup.ThreadProgress[part-1].URI
	}
	feed, err := getFeed(ctx, p.cfg.Handle, reconcileFeedLimit)
	if err != nil {
		return publishing.PostRef{}, false, false, fmt.Errorf("bluesky: reconcile: %w", err)
	}

	// Every own post counts towards how far back the feed reaches; only
	// those with the expected parent can match.
	var all, candidates []outboxreconcile.Post
	cids := make(map[string]string, len(feed))
	for _, item := range feed {
		// The author feed also lists the account's reposts of other people.
		if h := item.Post.Author.Handle; h != "" && !strings.EqualFold(h, p.cfg.Handle) {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, item.Post.Record.CreatedAt)
		if err != nil {
			continue
		}
		post := outboxreconcile.Post{ID: item.Post.URI, Text: item.Post.Record.Text, PostedAt: created}
		all = append(all, post)
		if replyParent(item.Post.Record) == parent {
			candidates = append(candidates, post)
			cids[item.Post.URI] = item.Post.CID
		}
	}
	if id, matched, _ := outboxreconcile.MatchPost(candidates, want, lookup.SentAt, lookup.ClockSkew); matched {
		return publishing.PostRef{URI: id, CID: cids[id]}, true, true, nil
	}
	return publishing.PostRef{}, false, reachesBack(all, lookup.SentAt.Add(-lookup.ClockSkew)), nil
}

// replyParent is the URI of the post rec replies to, or "" for a top-level
// post.
func replyParent(rec postRecord) string {
	if rec.Reply == nil {
		return ""
	}
	return rec.Reply.Parent.URI
}

// reachesBack reports whether the oldest of posts is no later than cutoff,
// the same coverage rule as outboxreconcile.MatchPost.
func reachesBack(posts []outboxreconcile.Post, cutoff time.Time) bool {
	if len(posts) == 0 {
		return false
	}
	oldest := posts[0].PostedAt
	for _, p := range posts[1:] {
		if p.PostedAt.Before(oldest) {
			oldest = p.PostedAt
		}
	}
	return !oldest.After(cutoff)
}
