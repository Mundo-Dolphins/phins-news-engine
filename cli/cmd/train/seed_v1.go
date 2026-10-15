package train

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

type v1SeedOptions struct {
	DBPath             string
	TelegramExportPath string
	OutPath            string
	// TeamSlug is the team token embedded in the v1 database's
	// team-specific discard reasons ("not_<slug>",
	// "substantive_<slug>_signal_missing:…", "…_without_<slug>_impact").
	TeamSlug string
}

// defaultV1TeamSlug is the --v1-team-slug default.
const defaultV1TeamSlug = "team"

type v1SeedSummary struct {
	Examples        int
	PublishExamples int
	RejectExamples  int
	PublishSkipped  int
	RejectSkipped   int
}

func newTrainSeedV1Cmd() *cobra.Command {
	opts := v1SeedOptions{}
	cmd := &cobra.Command{
		Use:   "seed-v1",
		Short: "Build a first editorial training dataset from v1 SQLite and Telegram export",
		RunE: func(cmd *cobra.Command, _ []string) error {
			summary, err := buildV1SeedDataset(cmd.Context(), opts)
			if err != nil {
				return err
			}
			cmd.Printf("wrote %d examples to %s (%d publish, %d reject; skipped %d publish, %d reject)\n",
				summary.Examples, opts.OutPath, summary.PublishExamples, summary.RejectExamples, summary.PublishSkipped, summary.RejectSkipped)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.DBPath, "v1-db", "../v1/data/sqlite/operational.db", "v1 SQLite operational.db path")
	cmd.Flags().StringVar(&opts.TelegramExportPath, "telegram-export", "../v1/data/channel_export.json", "v1 Telegram channel export JSON path")
	cmd.Flags().StringVar(&opts.OutPath, "out", "testdata/editorial_v1_seed_dataset.yml", "path to write the generated dataset")
	cmd.Flags().StringVar(&opts.TeamSlug, "v1-team-slug", defaultV1TeamSlug, "team token used in the v1 database's team-specific discard reasons (not_<slug>, ..._without_<slug>_impact)")
	return cmd
}

func buildV1SeedDataset(ctx context.Context, opts v1SeedOptions) (v1SeedSummary, error) {
	if strings.TrimSpace(opts.DBPath) == "" {
		return v1SeedSummary{}, fmt.Errorf("train seed-v1: --v1-db is required")
	}
	if strings.TrimSpace(opts.OutPath) == "" {
		return v1SeedSummary{}, fmt.Errorf("train seed-v1: --out is required")
	}

	telegramMessages, err := loadTelegramMessages(opts.TelegramExportPath)
	if err != nil {
		return v1SeedSummary{}, err
	}

	db, err := sql.Open("sqlite", opts.DBPath+"?_query_only=1&_busy_timeout=60000")
	if err != nil {
		return v1SeedSummary{}, fmt.Errorf("train seed-v1: open %s: %w", opts.DBPath, err)
	}
	defer db.Close()

	examples, summary, err := queryV1SeedExamples(ctx, db, telegramMessages, opts.TeamSlug)
	if err != nil {
		return v1SeedSummary{}, err
	}
	if len(examples) == 0 {
		return v1SeedSummary{}, fmt.Errorf("train seed-v1: no examples generated")
	}
	if err := editorialtrain.SaveDataset(opts.OutPath, examples); err != nil {
		return v1SeedSummary{}, err
	}
	summary.Examples = len(examples)
	return summary, nil
}

type telegramExportMessage struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

func loadTelegramMessages(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-provided tooling path.
	if err != nil {
		return nil, fmt.Errorf("train seed-v1: read telegram export %s: %w", path, err)
	}
	var messages []telegramExportMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("train seed-v1: parse telegram export %s: %w", path, err)
	}
	out := make(map[string]string, len(messages))
	for _, msg := range messages {
		if msg.ID == 0 {
			continue
		}
		out[fmt.Sprint(msg.ID)] = strings.TrimSpace(msg.Message)
	}
	return out, nil
}

func queryV1SeedExamples(ctx context.Context, db *sql.DB, telegramMessages map[string]string, teamSlug string) ([]editorialtrain.TrainingExample, v1SeedSummary, error) {
	slug := strings.TrimSpace(teamSlug)
	if slug == "" {
		slug = defaultV1TeamSlug
	}
	rows, err := db.QueryContext(ctx, `
SELECT
	id, title, url, normalized_url, source, source_handle, domain,
	source_published_at_utc, published_at_utc, fetched_at_utc,
	content_type, is_trusted_source, was_published, telegram_message_id,
	discard_reason, discard_subreason
FROM articles_local
WHERE title != ''
  AND (
    (was_published = 1 AND telegram_message_id != '')
    OR
    (was_published = 0 AND (
      discard_reason IN (?,'stale_content','historical_low_editorial_value','deterministic_editorial')
      OR (
        discard_reason = 'source_policy' AND (
          discard_subreason LIKE ?
          OR discard_subreason LIKE 'min_relevance_score:%'
          OR discard_subreason LIKE ?
          OR discard_subreason LIKE 'commerce_product_listing%'
          OR discard_subreason LIKE ?
          OR discard_subreason LIKE ?
          OR discard_subreason LIKE 'source_tier_noise:%'
          OR discard_subreason LIKE 'required_terms_missing:%'
        )
      )
    ))
  )
ORDER BY fetched_at_utc, id`,
		"not_"+slug,
		"substantive_"+slug+"_signal_missing:%",
		"generic_fantasy_without_"+slug+"_impact%",
		"rival_without_"+slug+"_impact%",
		"personal_anecdote_without_"+slug+"_impact%",
	)
	if err != nil {
		return nil, v1SeedSummary{}, fmt.Errorf("train seed-v1: query v1 articles: %w", err)
	}
	defer rows.Close()

	var examples []editorialtrain.TrainingExample
	var summary v1SeedSummary
	for rows.Next() {
		var row v1ArticleRow
		if err := rows.Scan(
			&row.ID, &row.Title, &row.URL, &row.NormalizedURL, &row.Source, &row.SourceHandle, &row.Domain,
			&row.SourcePublishedAtUTC, &row.PublishedAtUTC, &row.FetchedAtUTC,
			&row.ContentType, &row.IsTrustedSource, &row.WasPublished, &row.TelegramMessageID,
			&row.DiscardReason, &row.DiscardSubreason,
		); err != nil {
			return nil, v1SeedSummary{}, fmt.Errorf("train seed-v1: scan v1 article: %w", err)
		}

		example, ok := row.toTrainingExample(telegramMessages)
		if !ok {
			if row.WasPublished {
				summary.PublishSkipped++
			} else {
				summary.RejectSkipped++
			}
			continue
		}
		examples = append(examples, example)
		if example.Label == "publish" {
			summary.PublishExamples++
		} else {
			summary.RejectExamples++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, v1SeedSummary{}, fmt.Errorf("train seed-v1: iterate v1 articles: %w", err)
	}
	sort.SliceStable(examples, func(i, j int) bool {
		if !examples[i].PublishedAt.Equal(examples[j].PublishedAt) {
			return examples[i].PublishedAt.Before(examples[j].PublishedAt)
		}
		return examples[i].ArticleID < examples[j].ArticleID
	})
	return examples, summary, nil
}

type v1ArticleRow struct {
	ID                   int64
	Title                string
	URL                  string
	NormalizedURL        string
	Source               string
	SourceHandle         string
	Domain               string
	SourcePublishedAtUTC string
	PublishedAtUTC       string
	FetchedAtUTC         string
	ContentType          string
	IsTrustedSource      int
	WasPublished         bool
	TelegramMessageID    string
	DiscardReason        string
	DiscardSubreason     string
}

func (r v1ArticleRow) toTrainingExample(telegramMessages map[string]string) (editorialtrain.TrainingExample, bool) {
	fetchedAt, ok := parseV1Time(r.FetchedAtUTC)
	if !ok {
		return editorialtrain.TrainingExample{}, false
	}
	publishedAt := firstParsedTime(r.SourcePublishedAtUTC, r.PublishedAtUTC, r.FetchedAtUTC)
	if publishedAt.IsZero() {
		return editorialtrain.TrainingExample{}, false
	}

	label := "reject"
	reason := strings.Trim(strings.TrimSpace(r.DiscardReason)+":"+strings.TrimSpace(r.DiscardSubreason), ":")
	body := strings.TrimSpace(r.Title + "\n" + firstNonEmpty(r.NormalizedURL, r.URL))
	if r.WasPublished {
		message := strings.TrimSpace(telegramMessages[strings.TrimSpace(r.TelegramMessageID)])
		if message == "" {
			return editorialtrain.TrainingExample{}, false
		}
		label = "publish"
		reason = "v1_was_published_telegram"
		body = message
	}

	return editorialtrain.TrainingExample{
		ArticleID:       fmt.Sprintf("v1-%d", r.ID),
		Title:           strings.TrimSpace(r.Title),
		Body:            body,
		SourceID:        sourceIDFromV1(r.SourceHandle, r.Domain, r.Source),
		SourceType:      sourceTypeFromV1(r.Source),
		Domain:          domainFromV1(r.Domain, firstNonEmpty(r.NormalizedURL, r.URL)),
		ContentType:     contentTypeFromV1(r.Source, r.ContentType),
		PublishedAt:     publishedAt,
		FetchedAt:       fetchedAt,
		ClusterID:       "",
		IsPrimarySource: r.IsTrustedSource != 0,
		Label:           label,
		Reason:          reason,
		LabeledAt:       fetchedAt,
	}, true
}

func parseV1Time(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func firstParsedTime(values ...string) time.Time {
	for _, value := range values {
		if t, ok := parseV1Time(value); ok {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sourceTypeFromV1(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "rss":
		return "rss"
	case "website":
		return "web"
	case "twitter":
		return "twitter"
	case "bluesky":
		return "bluesky"
	case "instagram":
		return "instagram"
	case "reddit":
		return "reddit"
	case "youtube":
		return "youtube"
	default:
		return "other"
	}
}

func contentTypeFromV1(platform, contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "podcast_video":
		return "video"
	case "social_reaction":
		return "social_post"
	case "hard_news", "mock_draft", "prospect_card", "throwback", "":
		return "article"
	default:
		if sourceTypeFromV1(platform) == "twitter" || sourceTypeFromV1(platform) == "bluesky" || sourceTypeFromV1(platform) == "instagram" {
			return "social_post"
		}
		return "article"
	}
}

func domainFromV1(domain, rawURL string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain != "" {
		return domain
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

var nonSlugChar = regexp.MustCompile(`[^a-z0-9]+`)

func sourceIDFromV1(handle, domain, platform string) string {
	base := strings.TrimSpace(handle)
	if base == "" {
		base = strings.TrimSpace(domain)
	}
	if base == "" {
		base = strings.TrimSpace(platform)
	}
	base = strings.ToLower(base)
	base = strings.TrimPrefix(base, "@")
	base = strings.TrimPrefix(base, "#")
	base = strings.TrimSuffix(base, ".com")
	base = nonSlugChar.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		return "unknown"
	}
	return base
}
