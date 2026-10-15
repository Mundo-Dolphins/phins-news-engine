// Package backtestcandidate replays v2's own real historical article
// traffic (its own operational.db) through v2's real
// classification/dedup/clustering/editorial pipeline, wired against a
// candidate dictionary and/or candidate editorial model instead of the
// currently-promoted one, so a candidate artifact's likely publish/reject
// behavior can be audited before promotion — without touching the
// operational database. Reuses pipelinereplay (also used by host-provided historical backtests)
// for the actual pipeline wiring and replay mechanics; the two packages
// differ only in where historical traffic comes from and what ground
// truth it's compared against.
//
// Unlike a historical backtest (which replays v1's real fetch-cycle batches, grouped
// by v1's own cycle_id), v2's operational.db does not persist a per-cycle
// batch log for its own articles table, so each historical article is
// replayed as its own single-article synthetic cycle, in ascending
// fetched_at order. This still exercises real chronological
// dedup/clustering time-window behavior; it just can't reproduce the
// exact original fetch-batch composition. Ground truth for "was this
// actually published" is simply the article's own already-recorded
// Status in the real operational database — no external export needed,
// unlike a historical backtest's dependency on v1's Telegram channel export.
package backtestcandidate

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/pipelinereplay"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// maxHistoricalArticles bounds the single ListSince query backtestcandidate
// issues against the real operational.db — generous enough for months of
// real traffic at this project's volume, while keeping the query itself
// bounded rather than an unbounded full-table scan.
const maxHistoricalArticles = 200_000

// Config is backtestcandidate.Run's input. At least one of
// CandidateDictPath/CandidateModelPath must be non-empty.
type Config struct {
	Days               int
	DBPath             string // v2's own operational.db, opened read-only
	CandidateDictPath  string // optional: candidate dictionary YAML to evaluate
	CandidateModelPath string // optional: candidate editorial model directory to evaluate
	V2ConfigPath       string
	OutPath            string
	Progress           func(string)
}

// Row is one historical article's outcome, written as one CSV line.
type Row struct {
	Title              string
	URL                string
	FetchedAt          time.Time
	ActualStatus       string
	ActualReason       string
	ActuallyPublished  bool
	CandidateStatus    string
	CandidateReason    string
	CandidateRelevance float64
	CandidateEditorial float64
	CandidatePublished bool
	Outcome            string // "regression" | "new_pass" | "agree_publish" | "agree_reject"
}

// Report summarizes one backtestcandidate run.
type Report struct {
	TotalArticles int
	Regressions   int
	NewPasses     int
	AgreePublish  int
	AgreeReject   int
	Rows          []Row
}

// reachedOutbox reports whether status represents an article that reached
// (or, downstream of reaching, ProcessCycle accepted into) the publish
// outbox — used to derive ActuallyPublished from a real historical
// article's already-recorded Status, which (unlike a lab-DB replay) may
// have advanced past pending_publication via the real outbox delivery
// process production actually ran.
func reachedOutbox(status article.Status) bool {
	switch status {
	case article.StatusPendingPublication, article.StatusPublishing, article.StatusPublished,
		article.StatusPublishFailed, article.StatusPendingRetry:
		return true
	default:
		return false
	}
}

// Run replays the last cfg.Days days of v2's own operational.db articles
// through a real usecase.ProcessCycle wired via pipelinereplay.Wire against
// cfg's CandidateDictPath/CandidateModelPath, cross-checks each article's
// lab-replay outcome against its own already-recorded real Status, and
// writes the per-article detail to cfg.OutPath as CSV. See this package's
// doc comment for the scope and its differences from a historical backtest.
func Run(ctx context.Context, cfg Config) (Report, error) {
	progress := func(format string, args ...any) {
		if cfg.Progress != nil {
			cfg.Progress(fmt.Sprintf(format, args...))
		}
	}

	if cfg.CandidateDictPath == "" && cfg.CandidateModelPath == "" {
		return Report{}, errors.New("backtestcandidate: at least one of CandidateDictPath or CandidateModelPath is required")
	}

	progress("loading v2 config from %s", cfg.V2ConfigPath)
	v2cfg, err := config.Load(cfg.V2ConfigPath)
	if err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: load v2 config: %w", err)
	}

	since := time.Now().UTC().AddDate(0, 0, -cfg.Days)
	progress("loading historical articles from %s since %s", cfg.DBPath, since.Format(time.RFC3339))
	historical, err := loadHistoricalArticles(ctx, cfg.DBPath, since)
	if err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: load historical articles: %w", err)
	}
	progress("loaded %d historical articles", len(historical))

	progress("creating scratch v2 SQLite lab")
	labDir, err := os.MkdirTemp("", "engine-backtestcandidate-lab-")
	if err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: create lab dir: %w", err)
	}
	defer os.RemoveAll(labDir)

	labDB, err := infrasqlite.Open(labDir+"/lab.db", 5*time.Second)
	if err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: open lab db: %w", err)
	}
	defer labDB.Close()
	if err := infrasqlite.Migrate(ctx, labDB, migrations.FS); err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: migrate lab db: %w", err)
	}

	progress("wiring candidate v2 pipeline")
	pc, articles, fetcher, clock, err := pipelinereplay.Wire(v2cfg, labDB, pipelinereplay.Overrides{
		DictionaryPath: cfg.CandidateDictPath,
		ModelPath:      cfg.CandidateModelPath,
	})
	if err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: wire pipeline: %w", err)
	}

	report := Report{}
	progress("replaying %d historical articles through candidate pipeline", len(historical))
	for i, actual := range historical {
		clock.Set(actual.FetchedAt)
		fetcher.Set([]article.Article{actual})
		if _, err := pc.Run(ctx); err != nil {
			return Report{}, fmt.Errorf("backtestcandidate: process article %s: %w", actual.ID, err)
		}

		final, err := articles.FindByID(ctx, actual.ID)
		if err != nil {
			// A genuine "not found" means dedup claimed this article under
			// a different ID than its own historical one (see
			// the host's historical backtest, which handles it identically) — not reportable
			// per-row, but the replay itself still ran. Any other error is
			// a genuine infrastructure failure and must abort the run.
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return Report{}, fmt.Errorf("backtestcandidate: find article %s: %w", actual.ID, err)
		}

		row := Row{
			Title: actual.Title, URL: actual.CanonicalURL, FetchedAt: actual.FetchedAt,
			ActualStatus: string(actual.Status), ActualReason: actual.StatusReason,
			ActuallyPublished:  reachedOutbox(actual.Status),
			CandidateStatus:    string(final.Status),
			CandidateReason:    final.StatusReason,
			CandidateRelevance: final.RelevanceScore,
			CandidateEditorial: final.EditorialScore,
			CandidatePublished: final.Status == article.StatusPendingPublication,
		}
		switch {
		case row.ActuallyPublished && !row.CandidatePublished:
			row.Outcome = "regression"
			report.Regressions++
		case !row.ActuallyPublished && row.CandidatePublished:
			row.Outcome = "new_pass"
			report.NewPasses++
		case row.ActuallyPublished && row.CandidatePublished:
			row.Outcome = "agree_publish"
			report.AgreePublish++
		default:
			row.Outcome = "agree_reject"
			report.AgreeReject++
		}
		report.Rows = append(report.Rows, row)
		report.TotalArticles++

		if i == 0 || i == len(historical)-1 || (i+1)%100 == 0 {
			progress("replayed %d/%d articles", i+1, len(historical))
		}
	}

	progress("writing CSV report to %s", cfg.OutPath)
	if err := writeCSV(cfg.OutPath, report.Rows); err != nil {
		return Report{}, fmt.Errorf("backtestcandidate: write report: %w", err)
	}
	progress("done")
	return report, nil
}

// loadHistoricalArticles opens dbPath read-only and returns every article
// fetched at or after since, oldest first, capped at maxHistoricalArticles.
func loadHistoricalArticles(ctx context.Context, dbPath string, since time.Time) ([]article.Article, error) {
	db, err := infrasqlite.OpenReadOnly(dbPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("backtestcandidate: open %s read-only: %w", dbPath, err)
	}
	defer db.Close()

	repo := sqliteadapter.NewArticleRepository(db)
	return repo.ListSince(ctx, since, maxHistoricalArticles)
}

// writeCSV writes rows to path atomically: it builds the file at a
// temporary sibling path and renames it into place only on success, so a
// write or flush failure partway through never leaves the operator with a
// truncated report at path.
func writeCSV(path string, rows []Row) error {
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- path is an operator-provided CLI flag, not user input
	if err != nil {
		return err
	}
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	w := csv.NewWriter(f)

	header := []string{
		"title", "url", "fetched_at", "actual_status", "actual_reason", "actually_published",
		"candidate_status", "candidate_reason", "candidate_relevance_score", "candidate_editorial_score",
		"candidate_published", "outcome",
	}
	if err := w.Write(header); err != nil {
		_ = f.Close()
		return err
	}
	for _, r := range rows {
		record := []string{
			r.Title, r.URL, r.FetchedAt.UTC().Format(time.RFC3339),
			r.ActualStatus, r.ActualReason, fmt.Sprintf("%t", r.ActuallyPublished),
			r.CandidateStatus, r.CandidateReason,
			fmt.Sprintf("%.4f", r.CandidateRelevance), fmt.Sprintf("%.4f", r.CandidateEditorial),
			fmt.Sprintf("%t", r.CandidatePublished), r.Outcome,
		}
		if err := w.Write(record); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("backtestcandidate: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("backtestcandidate: rename temp file: %w", err)
	}
	return nil
}
