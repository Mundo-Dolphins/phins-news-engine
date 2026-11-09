package dictionarywiring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryminingfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
)

// ErrNoCompletedSession reports that Mine was given no session and no
// editorial review session has completed yet.
var ErrNoCompletedSession = errors.New("no completed session found")

// MineSetupError wraps a failure of one of Mine's own preparation steps
// (session lookup, matcher, mining policy, entity catalog), as opposed to an
// error passed through unchanged from the dictionary loader, the candidate
// store or the miner. The CLI prefixes only these with its command name.
type MineSetupError struct{ Err error }

// Error returns the wrapped error's text.
func (e *MineSetupError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *MineSetupError) Unwrap() error { return e.Err }

// MineReport lists the dictionary-candidate terms mined from one completed
// review session. Warnings are the non-fatal notices about optional inputs
// (league roster, English word list) that could not be used.
type MineReport struct {
	Session    string
	Candidates []dictionarymining.Candidate
	Warnings   []string
}

// MineSelection is Mine's report together with the dictionary and filter it
// mined against, which the CLI's interactive approval loop reuses.
type MineSelection struct {
	Report     MineReport
	Dictionary dictionary.Dictionary
	Filter     dictionarymining.FilterConfig
}

// Mine selects dictionary-candidate terms from sessionID (or, when empty, the
// most recently completed review session) without persisting anything. An
// error may come with a report whose Warnings were gathered before it.
func Mine(ctx context.Context, db *sql.DB, cfg *config.Config, sessionID string) (MineReport, error) {
	sel, err := SelectMineCandidates(ctx, db, cfg, sessionID)
	return sel.Report, err
}

// SelectMineCandidates does Mine's work and also returns the dictionary and
// filter used, for callers that go on to review the candidates. Like Mine, it
// persists nothing.
func SelectMineCandidates(ctx context.Context, db *sql.DB, cfg *config.Config, sessionID string) (MineSelection, error) {
	var sel MineSelection
	targetSession := sessionID
	if targetSession == "" {
		s, err := sqliteadapter.NewEditorialCycleSessionRepository(db).GetLatestCompleted(ctx)
		if err != nil {
			return sel, &MineSetupError{fmt.Errorf("%w and --session not given: %w", ErrNoCompletedSession, err)}
		}
		targetSession = s.ID
	}
	sel.Report.Session = targetSession

	dict, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		return sel, err
	}
	sel.Dictionary = dict
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		return sel, &MineSetupError{fmt.Errorf("prepare matcher: %w", err)}
	}

	filter, err := miningFilter(cfg)
	if err != nil {
		return sel, err
	}
	catalogPaths := cfg.Clustering.EntityCatalog
	if catalogPaths.ManualPath != "" || catalogPaths.GeneratedPath != "" {
		entities, err := entitycatalog.Load(catalogPaths.ManualPath, catalogPaths.GeneratedPath)
		if err != nil {
			return sel, &MineSetupError{fmt.Errorf("load entity catalog: %w", err)}
		}
		for _, entity := range entities {
			filter.KnownEntityTerms = append(filter.KnownEntityTerms, entity.CanonicalName)
			filter.KnownEntityTerms = append(filter.KnownEntityTerms, entity.Terms...)
		}
	}
	if p := catalogPaths.League.RosterPath; p != "" {
		if f, openErr := os.Open(p); openErr == nil { // #nosec G304 -- operator-configured roster path
			players, parseErr := nflverse.ParseRoster(f)
			_ = f.Close()
			if parseErr == nil {
				filter.KnownNames, filter.KnownSurnames = dictionaryminingfile.NamesFromRoster(players)
			} else {
				sel.Report.Warnings = append(sel.Report.Warnings, fmt.Sprintf("WARNING: nflverse roster %s unreadable (%v); people it covers are not filtered out", p, parseErr))
			}
		} else {
			sel.Report.Warnings = append(sel.Report.Warnings, fmt.Sprintf("WARNING: nflverse roster %s not found; people it covers are not filtered out", p))
		}
	}
	if p := cfg.DictionaryMining.EnglishWordsPath; p != "" {
		if words, wordsErr := dictionaryminingfile.LoadEnglishWords(p); wordsErr == nil {
			filter.EnglishWords = words
		} else {
			sel.Report.Warnings = append(sel.Report.Warnings, fmt.Sprintf("WARNING: english word list unavailable (%v); coined words (nicknames, slang) are not mined", wordsErr))
		}
	}
	rejectedTerms, err := sqliteadapter.NewDictionaryCandidateRepository(db).ListRejectedTerms(ctx)
	if err != nil {
		return sel, err
	}
	filter.IgnoredTerms = append(filter.IgnoredTerms, rejectedTerms...)
	sel.Filter = filter
	mined, err := dictionarymining.Select(ctx, dictionarymining.Deps{
		ReviewDecisions: sqliteadapter.NewReviewDecisionRepository(db), Articles: sqliteadapter.NewArticleRepository(db),
	}, targetSession, matcher, cfg.Normalization.Text.Policy(), filter)
	if err != nil {
		return sel, err
	}
	sel.Report.Candidates = mined
	return sel, nil
}

func miningFilter(cfg *config.Config) (dictionarymining.FilterConfig, error) {
	if cfg.DictionaryMining.Path == "" {
		return dictionarymining.FilterConfig{}, &MineSetupError{fmt.Errorf("dictionary_mining.path is required")}
	}
	filter, err := dictionaryminingfile.Load(cfg.DictionaryMining.Path)
	if err != nil {
		return dictionarymining.FilterConfig{}, &MineSetupError{fmt.Errorf("load mining policy: %w", err)}
	}
	return filter, nil
}
