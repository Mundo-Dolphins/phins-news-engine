package classifierdictionary

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/classifierproposalport"
)

type ArticleLister interface {
	ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error)
}

type AnalyzeOptions struct {
	Since            time.Time
	Limit            int
	Force            bool
	AnalyzerVersion  string
	MinOccurrences   int
	PositiveCategory string
	NegativeCategory string
	UnknownLimit     int
}

type AnalyzeResult struct {
	ArticlesConsidered       int
	ArticleProposalsUpserted int
	UnknownProposalsUpserted int
}

type candidate struct {
	value       string
	normalized  string
	kind        classifierproposal.Kind
	category    string
	occurrences int
	sources     map[string]string
	firstSeen   time.Time
	lastSeen    time.Time
}

var titleCaseSpanRE = regexp.MustCompile(`\b[A-Z][A-Za-z'’.-]+(?:\s+[A-Z][A-Za-z'’.-]+)+\b`)

// StopValues are Title-Case spans Analyze never proposes (exact match):
// generic league phrases by default. A host program adds its own (for
// example multi-word city names of the league's teams) once at startup.
// The team's own names need no entry: a span the dictionary already
// matches is skipped anyway.
var StopValues = map[string]bool{
	"Super Bowl": true,
	"NFL Draft":  true,
}

func Analyze(ctx context.Context, articles ArticleLister, repo classifierproposalport.ClassifierProposalRepository, dict dictionary.Dictionary, opts AnalyzeOptions, now time.Time) (AnalyzeResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 500
	}
	if opts.MinOccurrences <= 0 {
		opts.MinOccurrences = 2
	}
	if opts.PositiveCategory == "" {
		opts.PositiveCategory = "nfl_people_context"
	}
	if opts.NegativeCategory == "" {
		opts.NegativeCategory = "negative_other_sports_terms"
	}
	if opts.AnalyzerVersion == "" {
		opts.AnalyzerVersion = "classifier-dictionary-v1"
	}

	all, err := articles.ListSince(ctx, opts.Since, opts.Limit)
	if err != nil {
		return AnalyzeResult{}, fmt.Errorf("classifierdictionary: list articles: %w", err)
	}
	pending := all
	if !opts.Force {
		ids := make([]string, len(all))
		for i, a := range all {
			ids[i] = a.ID
		}
		unprocessed, err := repo.FilterUnprocessed(ctx, opts.AnalyzerVersion, ids)
		if err != nil {
			return AnalyzeResult{}, fmt.Errorf("classifierdictionary: filter unprocessed articles: %w", err)
		}
		keep := map[string]bool{}
		for _, id := range unprocessed {
			keep[id] = true
		}
		pending = pending[:0]
		for _, a := range all {
			if keep[a.ID] {
				pending = append(pending, a)
			}
		}
	}

	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		return AnalyzeResult{}, fmt.Errorf("classifierdictionary: prepare dictionary: %w", err)
	}

	result := AnalyzeResult{ArticlesConsidered: len(pending)}
	candidates := map[string]*candidate{}
	for _, a := range pending {
		kind, category, ok := signalForStatus(a.Status, opts.PositiveCategory, opts.NegativeCategory)
		if !ok {
			continue
		}
		for _, value := range extractTitleCaseSpans(a.Title + " " + a.Summary) {
			normalized := classifierproposal.Normalize(value)
			if normalized == "" || len(matcher.Find(value)) > 0 {
				continue
			}
			key := string(kind) + "\x00" + category + "\x00" + normalized
			c := candidates[key]
			if c == nil {
				c = &candidate{
					value: value, normalized: normalized, kind: kind, category: category,
					sources: map[string]string{}, firstSeen: a.FetchedAt, lastSeen: a.FetchedAt,
				}
				candidates[key] = c
			}
			c.occurrences++
			c.sources[a.ID] = a.Title
			if a.FetchedAt.Before(c.firstSeen) {
				c.firstSeen = a.FetchedAt
			}
			if a.FetchedAt.After(c.lastSeen) {
				c.lastSeen = a.FetchedAt
			}
		}
	}

	for _, c := range candidates {
		if c.occurrences < opts.MinOccurrences {
			continue
		}
		if _, err := repo.Upsert(ctx, proposalFromCandidate(c, opts.AnalyzerVersion, now)); err != nil {
			return result, fmt.Errorf("classifierdictionary: upsert proposal %q: %w", c.value, err)
		}
		result.ArticleProposalsUpserted++
	}

	ids := make([]string, len(pending))
	for i, a := range pending {
		ids[i] = a.ID
	}
	if err := repo.MarkArticlesProcessed(ctx, opts.AnalyzerVersion, ids, now); err != nil {
		return result, fmt.Errorf("classifierdictionary: mark articles processed: %w", err)
	}

	unknowns, err := repo.ListUnknownAggregates(ctx, opts.Since, opts.MinOccurrences, opts.UnknownLimit)
	if err != nil {
		return result, fmt.Errorf("classifierdictionary: list unknown aggregates: %w", err)
	}
	for _, u := range unknowns {
		if u.Normalized == "" || len(matcher.Find(u.Value)) > 0 {
			continue
		}
		p := classifierproposal.Proposal{
			Kind: classifierproposal.KindTerm, Category: opts.PositiveCategory, Value: u.Value,
			NormalizedValue: u.Normalized, Operation: "add", Confidence: 0.65, Occurrences: u.Occurrences,
			Status: classifierproposal.StatusPending, FirstSeen: u.FirstSeen, LastSeen: u.LastSeen,
			AnalyzerVersion: opts.AnalyzerVersion, CreatedAt: now,
			Metadata: map[string]string{"source_signal": "unknown_entity_occurrences"},
		}
		if u.ArticleID != "" {
			p.Sources = []classifierproposal.Source{{ArticleID: u.ArticleID, ArticleTitle: u.ArticleTitle}}
		}
		if _, err := repo.Upsert(ctx, p); err != nil {
			return result, fmt.Errorf("classifierdictionary: upsert unknown proposal %q: %w", u.Value, err)
		}
		result.UnknownProposalsUpserted++
	}

	return result, nil
}

func signalForStatus(status article.Status, positiveCategory, negativeCategory string) (classifierproposal.Kind, string, bool) {
	switch status {
	case article.StatusClustered, article.StatusPendingPublication, article.StatusPublishing, article.StatusPublished, article.StatusRetainedNoPublish:
		return classifierproposal.KindTerm, positiveCategory, true
	case article.StatusRejectedNotRelated:
		return classifierproposal.KindNegativeTerm, negativeCategory, true
	default:
		return "", "", false
	}
}

func extractTitleCaseSpans(text string) []string {
	raw := titleCaseSpanRE.FindAllString(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if StopValues[value] {
			continue
		}
		key := classifierproposal.Normalize(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func proposalFromCandidate(c *candidate, analyzerVersion string, now time.Time) classifierproposal.Proposal {
	p := classifierproposal.Proposal{
		Kind: c.kind, Category: c.category, Value: c.value, NormalizedValue: c.normalized,
		Operation: "add", Confidence: confidenceFor(c), Occurrences: c.occurrences,
		Status: classifierproposal.StatusPending, FirstSeen: c.firstSeen, LastSeen: c.lastSeen,
		AnalyzerVersion: analyzerVersion, CreatedAt: now,
		Metadata: map[string]string{"source_signal": "article_outcome"},
	}
	for articleID, title := range c.sources {
		p.Sources = append(p.Sources, classifierproposal.Source{ArticleID: articleID, ArticleTitle: title})
	}
	sort.Slice(p.Sources, func(i, j int) bool { return p.Sources[i].ArticleID < p.Sources[j].ArticleID })
	return p
}

func confidenceFor(c *candidate) float64 {
	conf := 0.5 + 0.05*float64(c.occurrences)
	if conf > 0.9 {
		return 0.9
	}
	return conf
}
