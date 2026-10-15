// app/usecase/league_rules.go
package usecase

import (
	"sync"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// leagueRulesCache merges a LeagueEntitySource into the shared clustering rules
// copy-on-write: the base rules (and their backing array) are never modified,
// and the merged slice is rebuilt only when the source hands out a new slice.
type leagueRulesCache struct {
	mu     sync.Mutex
	league []eventsignals.EntityRule // the source slice last merged
	merged eventsignals.Rules
}

func (c *leagueRulesCache) rulesFor(base eventsignals.Rules, src in.LeagueEntitySource) eventsignals.Rules {
	if src == nil {
		return base
	}
	league := src.Entities()
	if len(league) == 0 {
		return base
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.league) == len(league) && len(c.merged.Entities) > 0 && &c.league[0] == &league[0] {
		return c.merged
	}
	merged := base
	merged.Entities = make([]eventsignals.EntityRule, 0, len(base.Entities)+len(league))
	merged.Entities = append(merged.Entities, base.Entities...)
	merged.Entities = append(merged.Entities, league...)
	c.league, c.merged = league, merged
	return merged
}
