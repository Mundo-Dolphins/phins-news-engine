// Package eventclaims builds the operator views over event claims (ADR 0034):
// listing and showing claims with their outbox state, and checking whether a
// retry would be accepted. The CLI and the admin panel share it.
package eventclaims

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Store is what the views need: the claim repository plus the operator
// operations (outbox rows and retry).
type Store interface {
	out.EventClaimRepository
	out.EventClaimOperations
}

// View is one claim with the outbox state of its article.
type View struct {
	Handler, Key string
	State        out.EventClaimState
	ArticleID    string
	Data         json.RawMessage
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// Outbox holds the article's outbox rows, oldest first.
	Outbox []out.EventClaimOutboxRow
	// LastError is the last non-empty outbox error, or "" when there is none.
	LastError string
}

// ParseSince accepts a Go duration ("24h": that long ago) or a date
// ("2026-11-01", UTC midnight).
func ParseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: want a duration like 24h or a date like 2026-11-01", s)
}

// List returns the claims matching f, newest first, created at or after since
// (a zero since keeps all), each with its outbox rows and last error.
func List(ctx context.Context, s Store, f out.EventClaimFilter, since time.Time) ([]View, error) {
	claims, err := s.List(ctx, f)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(claims))
	for _, c := range claims {
		if !since.IsZero() && c.CreatedAt.Before(since) {
			continue
		}
		v, err := view(ctx, s, c)
		if err != nil {
			return nil, err
		}
		views = append(views, v)
	}
	return views, nil
}

// Show returns one claim with its outbox rows. The error wraps
// out.ErrEventClaimNotFound when there is no such claim.
func Show(ctx context.Context, s Store, handler, key string) (View, error) {
	c, found, err := s.Get(ctx, handler, key)
	if err != nil {
		return View{}, err
	}
	if !found {
		return View{}, fmt.Errorf("%w: %s/%s", out.ErrEventClaimNotFound, handler, key)
	}
	return view(ctx, s, c)
}

// CheckRetry applies, read-only, the checks RetryFailed makes and returns how
// many dead outbox rows a retry would re-enqueue. The refusals are the typed
// out.ErrEventClaimNotFound, ErrEventClaimNotFailed, ErrEventClaimInFlight and
// ErrEventClaimNothingToRetry.
func CheckRetry(ctx context.Context, s Store, handler, key string) (deadRows int, err error) {
	claim, found, err := s.Get(ctx, handler, key)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, out.ErrEventClaimNotFound
	}
	if claim.State != out.EventClaimFailed {
		return 0, out.ErrEventClaimNotFailed
	}
	rows, err := s.OutboxRows(ctx, claim.ArticleID)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		switch r.Status {
		case "dead":
			deadRows++
		case "delivered":
		default:
			return 0, out.ErrEventClaimInFlight
		}
	}
	if deadRows == 0 {
		return 0, out.ErrEventClaimNothingToRetry
	}
	return deadRows, nil
}

func view(ctx context.Context, s Store, c out.EventClaim) (View, error) {
	rows, err := s.OutboxRows(ctx, c.ArticleID)
	if err != nil {
		return View{}, err
	}
	return View{
		Handler: c.Handler, Key: c.Key, State: c.State, ArticleID: c.ArticleID, Data: c.Data,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Outbox: rows, LastError: lastError(rows),
	}, nil
}

func lastError(rows []out.EventClaimOutboxRow) string {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].LastError != "" {
			return rows[i].LastError
		}
	}
	return ""
}
