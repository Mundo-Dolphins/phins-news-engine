package evaluation

import (
	"context"
	"database/sql"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguebacktest"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/evalwiring"
)

// SetLeagueRun replaces the league wiring for a test.
func SetLeagueRun(f func(ctx context.Context, db *sql.DB, cfg *config.Config, opts evalwiring.LeagueOptions, now time.Time) (leaguebacktest.Report, error)) (restore func()) {
	old := leagueRun
	leagueRun = f
	return func() { leagueRun = old }
}
