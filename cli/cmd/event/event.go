// Package event holds the operator commands for event handler state
// (ADR 0034): inspecting, retrying and releasing event claims.
package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// dataSummaryMax bounds the data column of `claims list`.
const dataSummaryMax = 60

// NewCommand builds the `event` command.
func NewCommand() *cobra.Command {
	eventCmd := &cobra.Command{Use: "event", Short: "Event handler tooling"}
	claimsCmd := &cobra.Command{Use: "claims", Short: "Inspect and repair event claims (ADR 0034)"}
	claimsCmd.AddCommand(newListCommand(), newShowCommand(), newRetryCommand(), newReleaseCommand())
	eventCmd.AddCommand(claimsCmd)
	return eventCmd
}

// openStore opens and migrates the configured database.
func openStore(ctx context.Context) (*sql.DB, *sqliteadapter.EventClaimRepository, error) {
	cfg, err := config.Load(shared.ConfigPath)
	if err != nil {
		return nil, nil, err
	}
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return nil, nil, err
	}
	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, sqliteadapter.NewEventClaimRepository(db), nil
}

// parseSince accepts a Go duration ("24h": that long ago) or a date
// ("2026-11-01", UTC midnight).
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: want a duration like 24h or a date like 2026-11-01", s)
}

func summarize(data json.RawMessage) string {
	var buf strings.Builder
	buf.Grow(len(data))
	for _, r := range string(data) {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		buf.WriteRune(r)
	}
	s := buf.String()
	if r := []rune(s); len(r) > dataSummaryMax {
		return string(r[:dataSummaryMax-1]) + "…"
	}
	return s
}

func outboxSummary(rows []appout.EventClaimOutboxRow) string {
	if len(rows) == 0 {
		return "none"
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = r.Destination + ":" + r.Status
	}
	return strings.Join(parts, ",")
}

func lastError(rows []appout.EventClaimOutboxRow) string {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].LastError != "" {
			return rows[i].LastError
		}
	}
	return "-"
}

func newListCommand() *cobra.Command {
	var handler, state, since string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List event claims with their outbox status and last error",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter := appout.EventClaimFilter{Handler: handler, State: appout.EventClaimState(state)}
			if state != "" && !filter.State.Valid() {
				return fmt.Errorf("event claims list: unknown --state %q (claimed, published or failed)", state)
			}
			var sinceTime time.Time
			if since != "" {
				t, err := parseSince(since, clock.Real{}.Now())
				if err != nil {
					return fmt.Errorf("event claims list: %w", err)
				}
				sinceTime = t
			}
			db, repo, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			claims, err := repo.List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			printed := 0
			for _, c := range claims {
				if !sinceTime.IsZero() && c.CreatedAt.Before(sinceTime) {
					continue
				}
				rows, err := repo.OutboxRows(cmd.Context(), c.ArticleID)
				if err != nil {
					return err
				}
				article := c.ArticleID
				if article == "" {
					article = "-"
				}
				cmd.Printf("handler=%s key=%s state=%s article=%s outbox=%s data=%s last_error=%s\n",
					c.Handler, c.Key, c.State, article, outboxSummary(rows), summarize(c.Data), lastError(rows))
				printed++
			}
			cmd.Printf("%d claim(s)\n", printed)
			return nil
		},
	}
	cmd.Flags().StringVar(&handler, "handler", "", "only claims of this handler")
	cmd.Flags().StringVar(&state, "state", "", "only claims in this state: claimed, published or failed")
	cmd.Flags().StringVar(&since, "since", "", "only claims created since a duration ago (24h) or a date (2026-11-01)")
	return cmd
}

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <handler> <key>",
		Short: "Show one event claim with its full JSON data",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, repo, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			claim, found, err := repo.Get(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("event claims show: %w: %s/%s", appout.ErrEventClaimNotFound, args[0], args[1])
			}
			rows, err := repo.OutboxRows(cmd.Context(), claim.ArticleID)
			if err != nil {
				return err
			}
			type outboxJSON struct {
				Destination string `json:"destination"`
				Status      string `json:"status"`
				Attempts    int    `json:"attempts"`
				LastError   string `json:"last_error,omitempty"`
			}
			out := struct {
				Handler   string          `json:"handler"`
				Key       string          `json:"key"`
				State     string          `json:"state"`
				ArticleID string          `json:"article_id"`
				CreatedAt time.Time       `json:"created_at"`
				UpdatedAt time.Time       `json:"updated_at"`
				Data      json.RawMessage `json:"data"`
				Outbox    []outboxJSON    `json:"outbox"`
			}{claim.Handler, claim.Key, string(claim.State), claim.ArticleID, claim.CreatedAt, claim.UpdatedAt, claim.Data, []outboxJSON{}}
			for _, r := range rows {
				out.Outbox = append(out.Outbox, outboxJSON{r.Destination, r.Status, r.Attempts, r.LastError})
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
}

func newRetryCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "retry <handler> <key>",
		Short: "Re-enqueue the dead outbox rows of a failed claim and set it back to claimed",
		Long: "Retries a failed claim: its dead outbox rows (same destination and payload) go back to pending " +
			"and the claim returns to claimed. Refuses published claims and claims with an in-flight outbox row.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			handler, key := args[0], args[1]
			db, repo, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			if dryRun {
				return dryRunRetry(cmd, repo, handler, key)
			}
			n, err := repo.RetryFailed(cmd.Context(), handler, key, clock.Real{}.Now())
			if err != nil {
				return retryError(handler, key, err)
			}
			cmd.Printf("retry %s/%s: %d outbox row(s) re-enqueued, claim set to claimed\n", handler, key, n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check whether the retry would be accepted without changing anything")
	return cmd
}

func retryError(handler, key string, err error) error {
	switch {
	case errors.Is(err, appout.ErrEventClaimNotFound):
		return fmt.Errorf("event claims retry: no claim %s/%s", handler, key)
	case errors.Is(err, appout.ErrEventClaimNotFailed):
		return fmt.Errorf("event claims retry: refused, %s/%s is not failed (only failed claims can be retried)", handler, key)
	case errors.Is(err, appout.ErrEventClaimInFlight):
		return fmt.Errorf("event claims retry: refused, %s/%s still has an in-flight outbox row (let it finish or use the outbox repair command)", handler, key)
	case errors.Is(err, appout.ErrEventClaimNothingToRetry):
		return fmt.Errorf("event claims retry: refused, %s/%s has no dead outbox row to retry (release it instead)", handler, key)
	}
	return err
}

// dryRunRetry applies the same checks as RetryFailed, read-only.
func dryRunRetry(cmd *cobra.Command, repo *sqliteadapter.EventClaimRepository, handler, key string) error {
	claim, found, err := repo.Get(cmd.Context(), handler, key)
	if err != nil {
		return err
	}
	if !found {
		return retryError(handler, key, appout.ErrEventClaimNotFound)
	}
	if claim.State != appout.EventClaimFailed {
		return retryError(handler, key, appout.ErrEventClaimNotFailed)
	}
	rows, err := repo.OutboxRows(cmd.Context(), claim.ArticleID)
	if err != nil {
		return err
	}
	dead := 0
	for _, r := range rows {
		switch r.Status {
		case "dead":
			dead++
		case "delivered":
		default:
			return retryError(handler, key, appout.ErrEventClaimInFlight)
		}
	}
	if dead == 0 {
		return retryError(handler, key, appout.ErrEventClaimNothingToRetry)
	}
	cmd.Printf("dry run: would re-enqueue %d outbox row(s) of %s/%s and set the claim to claimed\n", dead, handler, key)
	return nil
}

func newReleaseCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "release <handler> <key>",
		Short: "Delete a claimed or failed claim so the handler can claim the key again",
		Long: "Deletes the claim (never a published one) so a corrected post can be created by the handler. " +
			"Requires --yes. Who and when are logged.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			handler, key := args[0], args[1]
			if !yes {
				return errors.New("event claims release: refused, this deletes the claim; pass --yes to confirm")
			}
			db, repo, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			released, err := repo.Release(cmd.Context(), handler, key)
			if errors.Is(err, appout.ErrEventClaimPublished) {
				return fmt.Errorf("event claims release: refused, %s/%s is published and would allow a duplicate post", handler, key)
			}
			if err != nil {
				return err
			}
			if !released {
				return fmt.Errorf("event claims release: no claim %s/%s", handler, key)
			}
			who := operator()
			at := clock.Real{}.Now().UTC()
			slog.Info("event claim released", "handler", handler, "key", key, "by", who, "at", at.Format(time.RFC3339))
			cmd.Printf("released %s/%s (by %s at %s)\n", handler, key, who, at.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deleting the claim")
	return cmd
}

// operator names who runs the command, for the release log.
func operator() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "unknown"
}
