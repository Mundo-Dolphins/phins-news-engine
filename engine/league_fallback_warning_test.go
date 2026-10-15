package engine

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestLeagueFallbackWarning(t *testing.T) {
	var cfg config.Config
	if got := leagueFallbackWarning(&cfg); got != "" {
		t.Fatalf("nothing enabled: warning = %q", got)
	}
	cfg.EntityExtraction.Fallback.Enabled = true
	if got := leagueFallbackWarning(&cfg); got != "" {
		t.Fatalf("only the fallback enabled: warning = %q", got)
	}
	cfg.Clustering.EntityCatalog.League.Mode = "mentions_only"
	got := leagueFallbackWarning(&cfg)
	if !strings.Contains(got, "fallback") || !strings.Contains(got, "league") {
		t.Fatalf("both enabled: warning = %q, want one naming both features", got)
	}
	cfg.EntityExtraction.Fallback.Enabled = false
	if got := leagueFallbackWarning(&cfg); got != "" {
		t.Fatalf("only the league enabled: warning = %q", got)
	}
}
