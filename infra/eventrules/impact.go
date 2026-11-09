// infra/eventrules/impact.go
package eventrules

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

// impactListLimit bounds the generic and the typed articles re-extracted by
// Impact.
const impactListLimit = 20000

var (
	// ErrNoRulesPath reports that clustering.rules_path is empty; errors.Is
	// matches it.
	ErrNoRulesPath = errors.New("clustering.rules_path is empty")
	// ErrInvalidDays reports a non-positive look-back window; errors.Is
	// matches it.
	ErrInvalidDays = errors.New("days must be positive")
)

// ImpactReport is the shadow report of the generated rules file over the
// recent articles. Nothing is written.
type ImpactReport struct {
	// GeneratedPath is the generated rules file compared against.
	GeneratedPath string
	// GeneratedMissing is true when that file does not exist.
	GeneratedMissing bool
	// Warnings are the notes from loading the generated file.
	Warnings []string
	// Days is the look-back window.
	Days int
	// Generic is the number of generic articles in the window and Changed
	// how many of them the generated rules would type.
	Generic, Changed int
	// Typed is the number of typed articles in the window and Retyped how
	// many of them the generated rules would change.
	Typed, Retyped int
	// GenericTruncated and TypedTruncated report that a sample hit the
	// article limit, so the counts are partial.
	GenericTruncated, TypedTruncated bool
	// Moved lists the titles of the changed generic articles by new type.
	Moved map[string][]string
	// RetypedBy lists the titles of the changed typed articles by
	// "old -> new".
	RetypedBy map[string][]string
}

// Impact compares the hand-written rules with the hand-written rules plus
// the generated file over the generic and typed articles of the last days
// days. It returns ErrInvalidDays when days is not positive and
// ErrNoRulesPath when clustering.rules_path is empty; it stops with the
// context's error once ctx is done.
func Impact(ctx context.Context, db *sql.DB, cfg *config.Config, days int, now time.Time) (ImpactReport, error) {
	var zero ImpactReport
	if cfg.Clustering.RulesPath == "" {
		return zero, &textError{msg: "clustering.rules_path is empty", sentinel: ErrNoRulesPath}
	}
	if days <= 0 {
		return zero, &textError{msg: "--days must be positive", sentinel: ErrInvalidDays}
	}
	base, err := HandWrittenRules(cfg)
	if err != nil {
		return zero, err
	}
	path := GeneratedPath(cfg)
	merged, warnings, err := clusteringrules.LoadWithGenerated(base, path)
	if err != nil {
		return zero, err
	}
	now = now.UTC()
	since := now.AddDate(0, 0, -days)
	lister := sqliteadapter.NewGenericArticleLister(db)
	samples, err := lister.ListGeneric(ctx, since, impactListLimit)
	if err != nil {
		return zero, err
	}
	typed, err := lister.ListTyped(ctx, since, impactListLimit)
	if err != nil {
		return zero, err
	}
	rep := ImpactReport{
		GeneratedPath:    path,
		Warnings:         warnings,
		Days:             days,
		Generic:          len(samples),
		Typed:            len(typed),
		GenericTruncated: len(samples) >= impactListLimit,
		TypedTruncated:   len(typed) >= impactListLimit,
		Moved:            map[string][]string{},
		RetypedBy:        map[string][]string{},
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		rep.GeneratedMissing = true
	}
	beforeRules, afterRules := base.ToEventSignalsRules(), merged.ToEventSignalsRules()
	for _, s := range samples {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		was := eventsignals.Extract(s.Article, beforeRules, now).EventType
		is := eventsignals.Extract(s.Article, afterRules, now).EventType
		if was == is {
			continue
		}
		rep.Changed++
		rep.Moved[string(is)] = append(rep.Moved[string(is)], s.Article.Title)
	}
	// Typed control: an already-typed article whose type changes is a
	// regression the checker should have caught.
	for _, s := range typed {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		was := eventsignals.Extract(s.Article, beforeRules, now).EventType
		is := eventsignals.Extract(s.Article, afterRules, now).EventType
		if was == is {
			continue
		}
		rep.Retyped++
		key := string(was) + " -> " + string(is)
		rep.RetypedBy[key] = append(rep.RetypedBy[key], s.Article.Title)
	}
	return rep, nil
}
