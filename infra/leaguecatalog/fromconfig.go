// infra/leaguecatalog/fromconfig.go
package leaguecatalog

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// FromConfig builds the provider for a configured mode; nil when the mode is off.
func FromConfig(cfg config.LeagueCatalogConfig, base []eventsignals.EntityRule, logger appout.Logger) *Provider {
	if !cfg.Enabled() {
		return nil
	}
	return New(Config{RosterPath: cfg.RosterPath, RulesPath: cfg.RulesPath, MaxAge: cfg.MaxAge()}, base, logger)
}
