// Package outbox holds the admin actions over the publication outbox:
// status, backlog, inspect, expire (preview and apply) and repair. They
// mirror the `telegram outbox status|backlog|inspect|expire|repair` CLI
// commands; `outbox reconcile` is not exposed because it needs the Telegram
// session.
package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const group = "outbox"

var errBadParams = errors.New("outbox: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

// statusOrder is the row order of `telegram outbox status`.
var statusOrder = []publishing.OutboxStatus{
	publishing.OutboxPending, publishing.OutboxLeased, publishing.OutboxSentUnconfirmed,
	publishing.OutboxRetryable, publishing.OutboxQuarantined, publishing.OutboxDelivered, publishing.OutboxDead,
}

type noParams struct{}

type backlogParams struct {
	Window string `json:"window" help:"trailing window for enqueue/delivery/lag/deferral figures" format:"duration" default:"1h"`
}

type inspectParams struct {
	Status string `json:"status" help:"which stuck items to list" enum:"sent_unconfirmed,quarantined" default:"quarantined"`
	Limit  int    `json:"limit" help:"maximum rows to list" default:"50"`
}

type expirePreviewParams struct {
	OlderThan   string `json:"older_than" help:"expire articles published more than this long ago (for example 24h)" format:"duration" required:"true"`
	ContentType string `json:"content_type" help:"only this content type" enum:"article,social_post,video,unknown"`
	Source      string `json:"source" help:"only this canonical source id (for example rss:espn)"`
	Limit       int    `json:"limit" help:"maximum candidates in one atomic batch" default:"500"`
}

type expireParams struct {
	OlderThan   string `json:"older_than" help:"expire articles published more than this long ago (for example 24h)" format:"duration" required:"true"`
	ContentType string `json:"content_type" help:"only this content type" enum:"article,social_post,video,unknown"`
	Source      string `json:"source" help:"only this canonical source id (for example rss:espn)"`
	Limit       int    `json:"limit" help:"maximum candidates in one atomic batch" default:"500"`
	Reason      string `json:"reason" help:"operator explanation recorded in the audit event" required:"true"`
}

type repairParams struct {
	ID        int64  `json:"id" help:"publication_outbox.id to repair" required:"true"`
	Action    string `json:"action" help:"deliver confirms the article and its cluster gate, retry re-enqueues, discard releases the cluster gate" enum:"deliver,retry,discard" required:"true"`
	ArticleID string `json:"article_id" help:"the item's own article_id; a mismatch with the row is rejected" required:"true"`
	Reason    string `json:"reason" help:"why this could not be resolved automatically" required:"true"`
}

// Actions returns the outbox actions: outbox.status, outbox.backlog,
// outbox.inspect, outbox.expire_preview, outbox.expire and outbox.repair.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "outbox.status", Group: group, Title: "Outbox status",
			Description: "Counts outbox items and the age of the oldest one per status.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runStatus,
		},
		{
			Name: "outbox.backlog", Group: group, Title: "Outbox backlog",
			Description: "Reports publication backlog, queue age and delivery lag against the configured thresholds. A critical level is shown in the summary, not raised as an error.",
			Kind:        action.KindRead, Modes: bothModes, Params: backlogParams{}, Run: runBacklog,
		},
		{
			Name: "outbox.inspect", Group: group, Title: "Inspect stuck outbox items",
			Description: "Lists sent_unconfirmed or quarantined outbox items (no token, no article body).",
			Kind:        action.KindRead, Modes: bothModes, Params: inspectParams{}, Run: runInspect,
		},
		{
			Name: "outbox.expire_preview", Group: group, Title: "Preview outbox expiration",
			Description: "Lists the pending or retryable publications that outbox.expire would expire, without changing anything.",
			Kind:        action.KindRead, Modes: bothModes, Params: expirePreviewParams{}, Run: runExpirePreview,
		},
		{
			Name: "outbox.expire", Group: group, Title: "Expire obsolete publications",
			Description: "Expires the pending or retryable publications matching the filters in one atomic batch and records an audit event.",
			Kind:        action.KindDestructive, Modes: bothModes, Params: expireParams{}, Run: runExpire,
		},
		{
			Name: "outbox.repair", Group: group, Title: "Repair stuck outbox item",
			Description: "Manually resolves a sent_unconfirmed or quarantined outbox item as delivered, retryable or discarded, with an audit event.",
			Kind:        action.KindDestructive, Modes: bothModes, Params: repairParams{}, Run: runRepair,
		},
	}
}

// clock adapts env.Now to the out.Clock port.
type clock func() time.Time

func (c clock) Now() time.Time { return c() }

// logger adapts a slog.Logger to the out.Logger port.
type logger struct{ l *slog.Logger }

func (g logger) Debug(msg string, kv ...any) { g.l.Debug(msg, kv...) }
func (g logger) Info(msg string, kv ...any)  { g.l.Info(msg, kv...) }
func (g logger) Warn(msg string, kv ...any)  { g.l.Warn(msg, kv...) }
func (g logger) Error(msg string, err error, kv ...any) {
	g.l.Error(msg, append([]any{"error", err}, kv...)...)
}

func badParam(format string, args ...any) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// parseDuration parses a duration param, naming it in the error.
func parseDuration(name, v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf("%s: %v", name, err), Err: err}
	}
	return d, nil
}

func runStatus(ctx context.Context, env action.Env, params any) (action.Result, error) {
	if _, ok := params.(*noParams); !ok {
		return action.Result{}, errBadParams
	}
	counts, err := sqliteadapter.NewOutboxRepository(env.DB).StatusCounts(ctx, env.Now())
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(statusOrder))
	for _, st := range statusOrder {
		c := counts[st]
		rows = append(rows, map[string]any{
			"status": string(st), "count": c.Count, "expired": c.ExpiredCount,
			"oldest_age": c.OldestAge.Round(time.Second).String(),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "status", Label: "Status"}, {Key: "count", Label: "Count"},
			{Key: "expired", Label: "Expired"}, {Key: "oldest_age", Label: "Oldest age"},
		},
		Rows: rows,
	}, nil
}

func runBacklog(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*backlogParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	window, err := parseDuration("window", p.Window)
	if err != nil {
		return action.Result{}, err
	}
	if window <= 0 {
		return action.Result{}, badParam("window must be positive")
	}
	snap, err := sqliteadapter.NewOutboxRepository(env.DB).BacklogSnapshot(ctx, env.Now(), window)
	if err != nil {
		return action.Result{}, err
	}
	report := publishing.NewBacklogReport(snap, env.Config.Publication.Backlog.Thresholds())
	return action.Result{
		Render:  "object",
		Object:  report,
		Summary: fmt.Sprintf("level=%s pending=%d", report.Level, report.BacklogCount),
	}, nil
}

func runInspect(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*inspectParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	repo := sqliteadapter.NewOutboxRepository(env.DB)
	var (
		items []appout.OutboxItem
		err   error
	)
	switch publishing.OutboxStatus(p.Status) {
	case publishing.OutboxSentUnconfirmed:
		items, err = repo.ListSentUnconfirmed(ctx, p.Limit, env.Now())
	case publishing.OutboxQuarantined:
		items, err = repo.ListQuarantined(ctx, p.Limit)
	default:
		return action.Result{}, badParam("status must be %s or %s, got %q", publishing.OutboxSentUnconfirmed, publishing.OutboxQuarantined, p.Status)
	}
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		sentAt := "-"
		if !it.SentAt.IsZero() {
			sentAt = it.SentAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, map[string]any{
			"id": it.ID, "article_id": it.ArticleID, "status": string(it.Status), "attempts": it.AttemptCount,
			"sent_message_id": orDash(it.SentMessageID), "ambiguous_reason": orDash(it.AmbiguousReason), "sent_at": sentAt,
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "id", Label: "ID"}, {Key: "article_id", Label: "Article"}, {Key: "status", Label: "Status"},
			{Key: "attempts", Label: "Attempts"}, {Key: "sent_message_id", Label: "Sent message"},
			{Key: "ambiguous_reason", Label: "Ambiguous reason"}, {Key: "sent_at", Label: "Sent at"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d item(s)", len(rows)),
	}, nil
}

// newExpire builds the expiration use case over env.DB.
func newExpire(env action.Env) *usecase.ExpireOutbox {
	db := env.DB
	return usecase.NewExpireOutbox(
		sqliteadapter.NewOutboxExpirationRepository(db),
		sqliteadapter.NewArticleRepository(db),
		sqliteadapter.NewClusterPublicationRepository(db),
		sqliteadapter.NewSourcePolicyCooldownRepository(db),
		sqliteadapter.NewDecisionRepository(db),
		sqliteadapter.NewUnitOfWork(db),
		clock(env.Now),
	)
}

// previewExpire parses the shared filters and lists the candidates.
func previewExpire(ctx context.Context, env action.Env, uc *usecase.ExpireOutbox, olderThan, contentType, source string, limit int) (usecase.ExpireOutboxPreview, error) {
	d, err := parseDuration("older_than", olderThan)
	if err != nil {
		return usecase.ExpireOutboxPreview{}, err
	}
	if d <= 0 {
		return usecase.ExpireOutboxPreview{}, badParam("older_than must be positive")
	}
	if limit <= 0 {
		return usecase.ExpireOutboxPreview{}, badParam("limit must be positive")
	}
	preview, err := uc.Preview(ctx, usecase.ExpireOutboxRequest{
		OlderThan: d, ContentType: article.ContentType(contentType), SourceID: source, Limit: limit,
	})
	if err != nil {
		// Preview validates the source id; its other failures are storage errors.
		if strings.Contains(err.Error(), "invalid source id") {
			return usecase.ExpireOutboxPreview{}, &action.Error{Code: action.CodeInvalidParams, Message: err.Error(), Err: err}
		}
		return usecase.ExpireOutboxPreview{}, err
	}
	return preview, nil
}

func runExpirePreview(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*expirePreviewParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	preview, err := previewExpire(ctx, env, newExpire(env), p.OlderThan, p.ContentType, p.Source, p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	now := env.Now()
	rows := make([]map[string]any, 0, len(preview.Candidates))
	for _, c := range preview.Candidates {
		rows = append(rows, map[string]any{
			"outbox_id": c.OutboxID, "article_id": c.ArticleID, "status": string(c.Status),
			"published_at": c.PublishedAt.UTC().Format(time.RFC3339), "expires_at": c.ExpiresAt.UTC().Format(time.RFC3339),
			"age": now.Sub(c.PublishedAt).Round(time.Second).String(), "content_type": string(c.ContentType),
			"source": orDash(c.SourceID), "title": orDash(c.Title),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "outbox_id", Label: "Outbox"}, {Key: "article_id", Label: "Article"}, {Key: "status", Label: "Status"},
			{Key: "published_at", Label: "Published"}, {Key: "expires_at", Label: "Expires"}, {Key: "age", Label: "Age"},
			{Key: "content_type", Label: "Type"}, {Key: "source", Label: "Source"}, {Key: "title", Label: "Title"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d candidate(s), cutoff %s", len(rows), preview.Cutoff.UTC().Format(time.RFC3339)),
	}, nil
}

func runExpire(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*expireParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if strings.TrimSpace(p.Reason) == "" {
		return action.Result{}, badParam("reason is required")
	}
	uc := newExpire(env)
	preview, err := previewExpire(ctx, env, uc, p.OlderThan, p.ContentType, p.Source, p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	applied := 0
	if len(preview.Candidates) > 0 {
		applied, err = uc.Apply(ctx, preview.Candidates, p.Reason)
		if err != nil {
			return action.Result{}, err
		}
	}
	env.Log.Warn("outbox publications expired", "applied", applied, "reason", p.Reason, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("applied=%d reason_code=%s", applied, usecase.OutboxExpiredReasonCode)}, nil
}

func runRepair(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*repairParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if strings.TrimSpace(p.Reason) == "" {
		return action.Result{}, badParam("reason is required")
	}
	db := env.DB
	repair := usecase.NewRepairOutbox(
		sqliteadapter.NewOutboxRepository(db),
		sqliteadapter.NewArticleRepository(db),
		sqliteadapter.NewClusterPublicationRepository(db),
		sqliteadapter.NewDecisionRepository(db),
		sqliteadapter.NewUnitOfWork(db),
		clock(env.Now),
		logger{env.Log},
	)
	err := repair.Run(ctx, p.ID, publishing.RepairAction(p.Action), p.ArticleID, p.Reason)
	switch {
	case err == nil:
	case errors.Is(err, appout.ErrNotRepairable):
		return action.Result{}, action.Conflict(err)
	case errors.Is(err, article.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return action.Result{}, action.NotFound(err)
	default:
		return action.Result{}, err
	}
	env.Log.Info("outbox item repaired", "id", p.ID, "action", p.Action, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("repaired outbox item %d: action=%s", p.ID, p.Action)}, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
