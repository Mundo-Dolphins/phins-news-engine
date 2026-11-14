// Package seenitemsactions holds the admin action that backfills seen_items
// from the existing articles history. It mirrors `seenitemsbackfill`.
package seenitemsactions

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/seenitemsbackfill"
)

// Actions returns the seen_items actions: seen_items.backfill.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "seen_items.backfill", Group: "seen_items", Title: "Backfill seen items",
			Description: "Backfills seen_items rows from the existing articles history, one per identity, newest fetch wins. Idempotent: identities that already have a row are skipped. Writes to the database and scans every article.",
			Kind:        action.KindWrite, Modes: []action.Mode{action.ModeLocal, action.ModeProduction},
			LongRunning: true, Exclusive: true, Run: runBackfill,
		},
	}
}

func runBackfill(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	res, err := seenitemsbackfill.Run(ctx, env.DB, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"candidates": res.Candidates, "inserted": res.Inserted, "skipped": res.Skipped,
		},
		Summary: res.Summary(),
	}, nil
}
