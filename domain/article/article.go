// Package article models a piece of content fetched from a Source and its
// processing lifecycle. It has no dependency on storage, HTTP, or config.
package article

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// ErrNotFound is the domain-level "no such article" sentinel. Repository
// adapters wrap it (alongside their own persistence-specific not-found
// error, e.g. sql.ErrNoRows) so application code can recognize "not
// found" via errors.Is without depending on a persistence package —
// see adapters/outbound/sqlite's wrapNotFound.
var ErrNotFound = errors.New("article: not found")

// ContentType classifies the shape of the content behind an Article.
type ContentType string

const (
	ContentTypeArticle    ContentType = "article"
	ContentTypeSocialPost ContentType = "social_post"
	ContentTypeVideo      ContentType = "video"
	ContentTypeUnknown    ContentType = "unknown"
)

// Status is the lifecycle stage of an Article inside the pipeline. The full
// state machine is documented in ADR 0005.
type Status string

const (
	StatusFetched            Status = "fetched"
	StatusNormalized         Status = "normalized"
	StatusRejectedValidation Status = "rejected_validation"
	StatusRejectedDuplicate  Status = "rejected_duplicate"
	StatusRejectedNotRelated Status = "rejected_not_related"
	StatusClusterCandidate   Status = "cluster_candidate"
	StatusClustered          Status = "clustered"
	StatusRejectedQuality    Status = "rejected_quality"
	StatusRejectedStale      Status = "rejected_stale"
	StatusPendingPublication Status = "pending_publication"
	StatusPublishing         Status = "publishing"
	StatusPublished          Status = "published"
	StatusPublishFailed      Status = "publish_failed"
	StatusPendingRetry       Status = "pending_retry"
	StatusDiscarded          Status = "discarded"
	// StatusRetainedNoPublish is a near-duplicate/confirmation that a
	// source policy allowed to proceed through clustering (Gate A) but
	// decided not to publish (Gate B) — e.g. an official confirmation
	// with no new information. Distinct from StatusRejectedDuplicate:
	// this article was a valid, clustered contribution (it may have
	// replaced a pending representative) that simply never reaches the
	// outbox. See domain/sourcepolicy.
	StatusRetainedNoPublish Status = "retained_no_publish"
)

// IsReprocessable reports whether an item whose previous terminal outcome
// was status may be reprocessed (dictionary/classifier/dedup/clustering/
// publish run again) after a material content revision. Only
// a status that never reached a "committed" outbound effect — every
// rejected_* status, and discarded — is eligible: a corrected title or
// body deserves a fresh classification pass. Any status that already
// reached or is en route to publication, or was deliberately retained
// without publishing, is never reprocessed by a content edit alone.
func IsReprocessable(s Status) bool {
	switch s {
	case StatusRejectedValidation, StatusRejectedDuplicate, StatusRejectedNotRelated,
		StatusRejectedQuality, StatusRejectedStale, StatusDiscarded:
		return true
	default:
		return false
	}
}

// Article is the central domain entity: a piece of content observed from a
// Source, alongside its processing state.
type Article struct {
	ID           string
	ExternalID   string
	OriginalURL  string
	CanonicalURL string
	Source       source.Source
	Domain       string
	Title        string
	Summary      string
	// Body is the article page's extracted body text, best-effort. Unlike
	// Title and Summary it is not authoritative: it may be empty when
	// extraction was skipped, failed, or produced too little text to
	// trust. Populated only for website-sourced articles — see
	// website.Fetcher's extractBody.
	Body              string
	Author            string
	Language          string
	PublishedAt       time.Time
	FetchedAt         time.Time
	ContentType       ContentType
	Metadata          map[string]string
	Status            Status
	StatusReason      string
	ClusterID         string
	ClassifierVersion string
	DictionaryVersion string
	EditorialScore    float64
	RelevanceScore    float64
	QualityThreshold  float64
	FreshnessMaxAge   time.Duration

	// PrimaryEntity, ClusterMemberRole, and IsTrustedSource are set once,
	// at accept time, from data ProcessCycle already computed during
	// clustering/classification (see process_cycle.go). They feed
	// PublishOutbox's publication policy (burst/same-topic/priority — see
	// domain/publishing) and the Telegram adapter's
	// update-labeling. PrimaryEntity is "" and
	// ClusterMemberRole is "" when clustering never ran for this article.
	PrimaryEntity     string
	ClusterMemberRole string
	IsTrustedSource   bool
}

// Normalize returns a copy of a with whitespace-trimmed text fields and a
// canonical URL filled in from the original URL when missing. Full URL
// canonicalization (case, tracking params, trailing slash, known-link
// resolution) is a separate, explicit ProcessCycle step using
// normalization.NormalizeURL — see ADR 0009 — so this domain package
// stays free of that config-shaped dependency.
func Normalize(a Article) Article {
	n := a
	n.Title = strings.TrimSpace(n.Title)
	n.Summary = strings.TrimSpace(n.Summary)
	n.Author = strings.TrimSpace(n.Author)
	if n.CanonicalURL == "" {
		n.CanonicalURL = n.OriginalURL
	}
	return n
}

// Validate checks a has the minimum data required to continue past
// normalization. It should be called on a Normalize()d article; a failure
// here is the RejectedValidation state.
func Validate(a Article) error {
	if a.CanonicalURL == "" {
		return fmt.Errorf("article: canonical_url is required")
	}
	if a.PublishedAt.IsZero() {
		return fmt.Errorf("article: published_at is required")
	}
	return nil
}
