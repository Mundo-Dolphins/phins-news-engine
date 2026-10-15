// Package leaguebacktest proves, on stored articles and without a database
// write, that league-scope entity rules change nothing but the entity mentions
// (phase 2 of the league gazetteer).
package leaguebacktest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// Config is one comparison run.
type Config struct {
	Articles []article.Article
	Base     eventsignals.Rules
	League   []eventsignals.EntityRule
	Now      time.Time
	// FrequencyPct lists entities mentioned in more than this percentage of
	// the articles as suspicious (default 0.5).
	FrequencyPct float64
}

// EntityFrequency is a league entity that matches in many articles.
type EntityFrequency struct {
	Name         string
	Articles     int
	Share        float64
	SampleTitles []string
}

// Report is the outcome of Run.
type Report struct {
	Articles, WithLeagueMention, LeagueMentions int
	InvariantViolations                         []string // article ids whose non-allowed fields differ; must be empty
	Suspicious                                  []EntityFrequency
	UnknownOff, UnknownOn                       int
	ExtractOff, ExtractOn                       time.Duration
}

// OK reports whether league rules changed nothing outside the allowed fields.
func (r Report) OK() bool { return len(r.InvariantViolations) == 0 }

func zeroAllowed(rep eventsignals.Representation) eventsignals.Representation {
	rep.Entities, rep.EntityMentions, rep.UnknownEntities = nil, nil, nil
	return rep
}

// Run extracts every article twice (without and with the league rules) and
// compares the results. It touches no database.
func Run(cfg Config) Report {
	threshold := cfg.FrequencyPct
	if threshold <= 0 {
		threshold = 0.5
	}
	withLeague := cfg.Base
	withLeague.Entities = append(append([]eventsignals.EntityRule(nil), cfg.Base.Entities...), cfg.League...)

	rep := Report{Articles: len(cfg.Articles)}
	freq := map[string]*EntityFrequency{}
	offReps := make([]eventsignals.Representation, len(cfg.Articles))

	start := time.Now()
	for i, a := range cfg.Articles {
		offReps[i] = eventsignals.Extract(a, cfg.Base, cfg.Now)
		rep.UnknownOff += len(offReps[i].UnknownEntities)
	}
	rep.ExtractOff = time.Since(start)

	start = time.Now()
	for i, a := range cfg.Articles {
		on := eventsignals.Extract(a, withLeague, cfg.Now)
		rep.UnknownOn += len(on.UnknownEntities)
		if !reflect.DeepEqual(zeroAllowed(offReps[i]), zeroAllowed(on)) {
			rep.InvariantViolations = append(rep.InvariantViolations, a.ID)
		}
		seen := false
		for _, m := range on.EntityMentions {
			if m.Scope != eventsignals.ScopeLeague {
				continue
			}
			rep.LeagueMentions++
			seen = true
			f := freq[m.Name]
			if f == nil {
				f = &EntityFrequency{Name: m.Name}
				freq[m.Name] = f
			}
			f.Articles++
			if len(f.SampleTitles) < 3 {
				f.SampleTitles = append(f.SampleTitles, a.Title)
			}
		}
		if seen {
			rep.WithLeagueMention++
		}
	}
	rep.ExtractOn = time.Since(start)

	for _, f := range freq {
		f.Share = float64(f.Articles) / float64(len(cfg.Articles))
		if f.Share*100 > threshold {
			rep.Suspicious = append(rep.Suspicious, *f)
		}
	}
	sort.Slice(rep.Suspicious, func(i, j int) bool {
		if rep.Suspicious[i].Articles != rep.Suspicious[j].Articles {
			return rep.Suspicious[i].Articles > rep.Suspicious[j].Articles
		}
		return rep.Suspicious[i].Name < rep.Suspicious[j].Name
	})
	return rep
}

// Text renders the report for the CLI.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "articles: %d\n", r.Articles)
	if r.OK() {
		b.WriteString("invariant: OK (nothing outside Entities, EntityMentions and UnknownEntities changed)\n")
	} else {
		fmt.Fprintf(&b, "invariant: VIOLATED for %d articles, e.g. %v\n", len(r.InvariantViolations), r.InvariantViolations[:min(5, len(r.InvariantViolations))])
	}
	fmt.Fprintf(&b, "league mentions: %d in %d articles\n", r.LeagueMentions, r.WithLeagueMention)
	fmt.Fprintf(&b, "unknown entities: %d without league rules, %d with\n", r.UnknownOff, r.UnknownOn)
	var perOff, perOn time.Duration
	if r.Articles > 0 {
		perOff, perOn = r.ExtractOff/time.Duration(r.Articles), r.ExtractOn/time.Duration(r.Articles)
	}
	fmt.Fprintf(&b, "extract time per article: %s without, %s with\n", perOff, perOn)
	for _, s := range r.Suspicious {
		fmt.Fprintf(&b, "  frequent: %s in %d articles (%.2f%%) e.g. %q\n", s.Name, s.Articles, s.Share*100, s.SampleTitles)
	}
	return b.String()
}
