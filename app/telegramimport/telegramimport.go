// Package telegramimport matches Telegram channel messages back to known
// local articles by URL and writes a human_labels row for each match.
// Unmatched messages are only counted, never stored as a synthetic example.
package telegramimport

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// urlPattern captures (not strips) the first URL in a message's text —
// every existing URL regex elsewhere in this codebase strips URLs for
// display/dedup purposes; this is the first one that extracts one for
// lookup use.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// ExtractURL returns the first bare URL found in text, if any — the same
// extraction Import uses to match a channel message to a local article,
// exported for reuse by outboxreconcile's channel-history matching (see
// ADR 0030).
func ExtractURL(text string) (string, bool) {
	raw := urlPattern.FindString(text)
	return raw, raw != ""
}

// Deps are the ports Import reads from and writes through.
type Deps struct {
	Articles out.ArticleRepository
	Labels   out.HumanLabelRepository
	Clock    out.Clock
}

// Result summarizes one Import run — including a partial run that ended
// early via the returned error: every field reflects messages actually
// processed before an abort, never zeroed out on failure, so a caller can
// report what happened and decide whether/how to resume.
type Result struct {
	Processed    int // messages looked at before this run ended (successfully or not)
	Imported     int // article found, no prior label — a new label written (or would be, in dry-run)
	AlreadyKnown int // article found, but already has a human_labels row
	Unmatched    int // URL found and normalized, but matched no local article (a true "not found")
	Skipped      int // message had no URL, or the URL failed to normalize — never queried the repository
	Failed       int // a repository error other than "not found" aborted this run (fail-fast: at most 1)
}

// Import matches each message to a known local article by URL and writes
// a "publish"/"historical_import" human_labels row for every match that
// doesn't already have one. dryRun computes the identical Result but
// never calls Labels.Label.
//
// Only a genuine "not found" (deps.Articles.FindByCanonicalURL returning
// an error satisfying errors.Is(err, article.ErrNotFound)) counts toward
// Unmatched. Any other repository error — a locked or corrupt database,
// an I/O failure, a canceled/timed-out context — aborts the run
// immediately: continuing to query a database in that state would just
// produce more of the same failure. Import returns the Result accumulated
// so far alongside a wrapped, non-nil error in that case; it is safe to
// fix the underlying problem and re-run Import over the same (or a
// superset of the) messages afterward — already-labeled articles are
// never re-labeled, so a re-run never duplicates a human_labels row.
func Import(
	ctx context.Context, deps Deps, messages []telegramimport.Message,
	urlCfg normalization.URLConfig, resolvers []normalization.KnownLinkResolver, dryRun bool,
) (Result, error) {
	var result Result

	for _, m := range messages {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("telegramimport: canceled after processing %d/%d message(s): %w", result.Processed, len(messages), err)
		}
		result.Processed++

		raw, ok := ExtractURL(m.Text)
		if !ok {
			result.Skipped++
			continue
		}
		canonical, err := normalization.NormalizeURL(raw, urlCfg, resolvers)
		if err != nil {
			result.Skipped++
			continue
		}

		a, err := deps.Articles.FindByCanonicalURL(ctx, canonical)
		if err != nil {
			if errors.Is(err, article.ErrNotFound) {
				result.Unmatched++
				continue
			}
			result.Failed++
			return result, fmt.Errorf("telegramimport: match message %d: %w", m.ID, err)
		}

		existing, err := deps.Labels.ListByArticle(ctx, a.ID)
		if err != nil {
			result.Failed++
			return result, fmt.Errorf("telegramimport: list existing labels for %s: %w", a.ID, err)
		}
		if len(existing) > 0 {
			result.AlreadyKnown++
			continue
		}

		if dryRun {
			result.Imported++
			continue
		}
		if err := deps.Labels.Label(ctx, training.Example{
			ArticleID: a.ID, Label: "publish", Reason: "historical_import", LabeledAt: deps.Clock.Now(), Source: "telegramimport",
		}); err != nil {
			result.Failed++
			return result, fmt.Errorf("telegramimport: save label for %s: %w", a.ID, err)
		}
		result.Imported++
	}
	return result, nil
}
