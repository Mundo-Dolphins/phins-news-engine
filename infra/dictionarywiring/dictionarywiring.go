// Package dictionarywiring holds the dictionary operations (validate,
// inspect, history, regression-check; diff in diff.go, rollback in
// rollback.go, candidate mining in mine.go) so the CLI and the admin panel
// share one implementation. It must not import
// the cli tree: the admin binary consumes it without cobra globals.
//
// Errors carry no command prefix; callers add their own.
package dictionarywiring

import (
	"context"
	"database/sql"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Validate loads the dictionary at path and returns it with its validation
// issues. A load failure is an error; error-severity issues are not (see
// HasErrors).
func Validate(path string) (dictionary.Dictionary, []dictionary.Issue, error) {
	d, err := dictionaryfile.Load(path)
	if err != nil {
		return dictionary.Dictionary{}, nil, err
	}
	return d, dictionary.Validate(d), nil
}

// HasErrors reports whether any issue has error severity.
func HasErrors(issues []dictionary.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == dictionary.SeverityError {
			return true
		}
	}
	return false
}

// CategoryStats summarises one dictionary category.
type CategoryStats struct {
	Name           string
	Weight         float64
	StrongOverride bool
	Entries        int
	Aliases        int
}

// InspectReport is the summary statistics of a dictionary file.
type InspectReport struct {
	Version      string
	Name         string
	Language     string
	Categories   []CategoryStats
	TotalEntries int
	TotalAliases int
	Decision     dictionary.Decision
}

// Inspect loads the dictionary at path and returns its summary statistics.
func Inspect(path string) (InspectReport, error) {
	d, err := dictionaryfile.Load(path)
	if err != nil {
		return InspectReport{}, err
	}
	r := InspectReport{Version: d.Version, Name: d.Name, Language: d.Language, Decision: d.Decision}
	for _, cat := range d.Categories {
		aliases := 0
		for _, e := range cat.Entries {
			aliases += len(e.Aliases)
		}
		r.TotalEntries += len(cat.Entries)
		r.TotalAliases += aliases
		r.Categories = append(r.Categories, CategoryStats{
			Name: cat.Name, Weight: cat.Weight, StrongOverride: cat.StrongOverride,
			Entries: len(cat.Entries), Aliases: aliases,
		})
	}
	return r, nil
}

// History returns every recorded dictionary version, newest first.
func History(ctx context.Context, db *sql.DB) ([]out.DictionaryHistoryRecord, error) {
	return sqliteadapter.NewDictionaryHistoryRepository(db).List(ctx)
}

// HistoryVersion returns one version's ledger record together with its
// on-disk artifacts under dictionaryhistory.DefaultDir. An unknown version is
// a wrapped sql.ErrNoRows.
func HistoryVersion(ctx context.Context, db *sql.DB, version string) (out.DictionaryHistoryRecord, dictionaryhistory.Artifacts, error) {
	rec, err := sqliteadapter.NewDictionaryHistoryRepository(db).Get(ctx, version)
	if err != nil {
		return out.DictionaryHistoryRecord{}, dictionaryhistory.Artifacts{}, err
	}
	artifacts, err := dictionaryhistory.ReadVersion(dictionaryhistory.DefaultDir, version)
	if err != nil {
		return out.DictionaryHistoryRecord{}, dictionaryhistory.Artifacts{}, err
	}
	return rec, artifacts, nil
}

// RegressionCheck runs the seed regression dataset against the previous and
// candidate dictionaries and reports the classification flips.
func RegressionCheck(previousPath, candidatePath string) (regressioncheck.Report, error) {
	prev, err := dictionaryfile.Load(previousPath)
	if err != nil {
		return regressioncheck.Report{}, err
	}
	cand, err := dictionaryfile.Load(candidatePath)
	if err != nil {
		return regressioncheck.Report{}, err
	}
	return regressioncheck.Run(prev, cand)
}
