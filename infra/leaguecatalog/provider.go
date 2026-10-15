// Package leaguecatalog supplies league-scope entity rules to the production
// extractor, built from the nflverse roster file. It never fails: any problem
// disables the rules and ingestion continues.
package leaguecatalog

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Config locates the inputs of the league entities.
type Config struct {
	RosterPath string
	RulesPath  string
	MaxAge     time.Duration // 0 = a roster never goes stale
}

// Status describes the provider for logs and operators.
type Status struct {
	Loaded    bool
	Rules     int
	RosterAge time.Duration
	Reason    string // why the feature is disabled; empty when Loaded
}

// Provider supplies league-scope entity rules built from the nflverse roster.
type Provider struct {
	cfg    Config
	logger appout.Logger
	teams  map[string]string // normalised catalog team name -> team id
	taken  map[string]bool   // team catalog ids and slugged canonical names

	mu        sync.RWMutex
	entities  []eventsignals.EntityRule
	lastKey   fileTimes // inputs of the last build attempt
	attempted bool      // lastKey is meaningful
	status    Status
	lastWarn  string // the reason already warned about, to log each transition once
}

// New builds a provider. base is the team catalog (teams and people), used
// for team ids and to keep league rules from shadowing a catalog entity.
func New(cfg Config, base []eventsignals.EntityRule, logger appout.Logger) *Provider {
	p := &Provider{cfg: cfg, logger: logger, teams: map[string]string{}, taken: map[string]bool{}}
	for _, r := range base {
		p.taken[r.ID] = true
		p.taken[eventsignals.Slug(r.CanonicalName)] = true
		if r.Kind != eventsignals.EntityKindTeam && r.Kind != eventsignals.EntityKindCompetition {
			p.taken[leaguegazetteer.IdentityKey(r.CanonicalName)] = true
			for _, t := range r.Terms {
				if strings.Contains(strings.TrimSpace(t), " ") { // a full name; a lone surname must not block a player
					p.taken[leaguegazetteer.IdentityKey(t)] = true
				}
			}
		}
		if r.Kind == eventsignals.EntityKindTeam {
			p.teams[entityproposal.Normalize(r.CanonicalName)] = r.ID
		}
	}
	return p
}

// fileTimes identifies the inputs of one build.
type fileTimes struct{ roster, rules time.Time }

// Refresh re-checks the roster and rules files (one stat each) and rebuilds the
// rules only when either file changed. A build that failed is remembered by the
// same file times, so a corrupt file is not re-parsed on every cycle. Call it
// once per processing cycle.
func (p *Provider) Refresh(now time.Time) {
	info, err := os.Stat(p.cfg.RosterPath)
	if err != nil {
		p.disable(fmt.Sprintf("roster unavailable: %v", err), nil)
		return
	}
	age := now.Sub(info.ModTime())
	if p.cfg.MaxAge > 0 && age > p.cfg.MaxAge {
		// The reason is stable (no age in it) so the warning is logged once per
		// transition, not once per hour.
		p.disable(fmt.Sprintf("roster older than max_age (%s)", p.cfg.MaxAge), nil)
		return
	}
	rinfo, err := os.Stat(p.cfg.RulesPath)
	if err != nil {
		p.disable(fmt.Sprintf("league rules unavailable: %v", err), nil)
		return
	}
	key := fileTimes{roster: info.ModTime(), rules: rinfo.ModTime()}

	p.mu.Lock()
	if p.attempted && key == p.lastKey {
		if p.status.Loaded {
			p.status.RosterAge = age
		}
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	rules, err := p.build()
	if err != nil {
		p.disable(err.Error(), &key)
		return
	}
	p.mu.Lock()
	recovered := !p.status.Loaded && p.lastWarn != ""
	p.entities, p.lastKey, p.attempted = rules, key, true
	p.status = Status{Loaded: true, Rules: len(rules), RosterAge: age}
	p.lastWarn = ""
	p.mu.Unlock()
	if recovered {
		p.logger.Info("league entities enabled again", "rules", len(rules), "roster_age_hours", int(age.Hours()))
	} else {
		p.logger.Info("league entities loaded", "rules", len(rules), "roster_age_hours", int(age.Hours()))
	}
}

func (p *Provider) build() ([]eventsignals.EntityRule, error) {
	f, err := os.Open(p.cfg.RosterPath) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return nil, fmt.Errorf("roster unreadable: %w", err)
	}
	defer f.Close()
	rows, err := nflverse.ParseRoster(f)
	if err != nil {
		return nil, fmt.Errorf("roster invalid: %w", err)
	}
	rules, err := leaguerules.Load(p.cfg.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("league rules invalid: %w", err)
	}
	players := make([]leaguegazetteer.Player, len(rows))
	for i, r := range rows {
		players[i] = leaguegazetteer.Player{
			FullName: r.FullName, FirstName: r.FirstName, LastName: r.LastName, FootballName: r.FootballName,
			TeamAbbr: r.Team, Position: r.Position, Status: r.Status, GSISID: r.GSISID,
		}
	}
	idx, err := leaguegazetteer.New(players, rules)
	if err != nil {
		return nil, fmt.Errorf("league index invalid: %w", err)
	}
	teamIDs := map[string]string{}
	for _, abbr := range idx.TeamAbbrs() {
		if id := p.teams[entityproposal.Normalize(idx.TeamName(abbr))]; id != "" {
			teamIDs[abbr] = id
		}
	}
	return idx.ExportRules(teamIDs, p.taken), nil
}

// disable switches the feature off and warns once per distinct reason. A
// non-nil attempted key records a failed build so it is not retried until a
// file changes; nil (a missing or stale input) forgets it.
func (p *Provider) disable(reason string, attempted *fileTimes) {
	p.mu.Lock()
	warn := p.lastWarn != reason
	p.entities = nil
	p.status = Status{Reason: reason}
	p.attempted = attempted != nil
	if attempted != nil {
		p.lastKey = *attempted
	}
	if warn {
		p.lastWarn = reason
	}
	p.mu.Unlock()
	if warn {
		p.logger.Warn("league entities disabled; ingestion continues without them", "reason", reason)
	}
}

// Entities returns the league rules, or nil when disabled. The slice is
// immutable; it is the same one until the roster file changes.
func (p *Provider) Entities() []eventsignals.EntityRule {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entities
}

// Status reports the current state.
func (p *Provider) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}
