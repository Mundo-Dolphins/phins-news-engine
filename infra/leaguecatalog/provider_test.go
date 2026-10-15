// infra/leaguecatalog/provider_test.go
package leaguecatalog_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

type recLogger struct {
	mu    sync.Mutex
	warns int
	infos int
}

func (l *recLogger) Debug(string, ...any)        {}
func (l *recLogger) Info(string, ...any)         { l.mu.Lock(); l.infos++; l.mu.Unlock() }
func (l *recLogger) Warn(string, ...any)         { l.mu.Lock(); l.warns++; l.mu.Unlock() }
func (l *recLogger) Error(string, error, ...any) {}

const rosterHeader = "full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\n"

func writeRoster(t *testing.T, dir string, mtime time.Time, rows ...string) string {
	t.Helper()
	p := filepath.Join(dir, "roster.csv")
	body := rosterHeader
	for _, r := range rows {
		body += r + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func repoRules(t *testing.T) string { return repofs.ExampleConfig(t, "league_alias_rules.yml") }

func baseCatalog() []eventsignals.EntityRule {
	// every team the tests use as a catalog team entity, plus a primary-team player
	var out []eventsignals.EntityRule
	for _, name := range []string{"Shelbyville Sharks", "Springfield Isotopes", "Capital City Capitals"} {
		out = append(out, eventsignals.EntityRule{ID: eventsignals.Slug(name), CanonicalName: name, Kind: eventsignals.EntityKindTeam, Terms: []string{name}})
	}
	out = append(out, eventsignals.EntityRule{ID: "jordan-sample", CanonicalName: "Jordan Sample", Kind: eventsignals.EntityKindPlayer, Terms: []string{"jordan sample"}})
	return out
}

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func refreshed(cfg leaguecatalog.Config, log *recLogger) *leaguecatalog.Provider {
	p := leaguecatalog.New(cfg, baseCatalog(), log)
	p.Refresh(now)
	return p
}

func TestProvider_LoadsFreshRosterAsLeagueRules(t *testing.T) {
	dir := t.TempDir()
	path := writeRoster(t, dir, now.Add(-24*time.Hour),
		"Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5", "Jordan Sample,Jordan,Sample,Jordan,SPR,QB,ACT,g2,5")
	p := refreshed(leaguecatalog.Config{RosterPath: path, RulesPath: repoRules(t), MaxAge: 7 * 24 * time.Hour}, &recLogger{})
	got := p.Entities()
	if len(got) != 1 || got[0].CanonicalName != "Avery Placeholder" || got[0].Scope != eventsignals.ScopeLeague {
		t.Fatalf("Entities() = %+v; want only Avery Placeholder (Jordan Sample is a primary-team catalog entity and must not be shadowed)", got)
	}
	if got[0].TeamID != "shelbyville-sharks" {
		t.Fatalf("TeamID = %q, want the catalog team id", got[0].TeamID)
	}
	if st := p.Status(); !st.Loaded || st.Rules != 1 || st.RosterAge < 23*time.Hour {
		t.Fatalf("Status() = %+v", st)
	}
}

func TestProvider_FailsOpenAndWarnsOncePerTransition(t *testing.T) {
	dir := t.TempDir()
	log := &recLogger{}
	cfg := leaguecatalog.Config{RosterPath: filepath.Join(dir, "missing.csv"), RulesPath: repoRules(t), MaxAge: 7 * 24 * time.Hour}
	p := leaguecatalog.New(cfg, baseCatalog(), log)
	for i := 0; i < 5; i++ {
		p.Refresh(now)
	}
	if len(p.Entities()) != 0 || p.Status().Loaded || p.Status().Reason == "" {
		t.Fatalf("a missing roster must disable league entities with a reason: %+v", p.Status())
	}
	if log.warns != 1 {
		t.Fatalf("warns = %d, want exactly one for five identical failing cycles", log.warns)
	}
	made := writeRoster(t, t.TempDir(), now.Add(-time.Hour), "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	if err := os.Rename(made, cfg.RosterPath); err != nil {
		t.Fatal(err)
	}
	p.Refresh(now)
	if len(p.Entities()) != 1 || log.infos == 0 {
		t.Fatalf("recovery: entities=%d infos=%d; want the roster loaded and an info line", len(p.Entities()), log.infos)
	}
}

func TestProvider_StaleCorruptAndBadRulesDisableWithoutPanicking(t *testing.T) {
	dir := t.TempDir()
	stale := writeRoster(t, dir, now.Add(-30*24*time.Hour), "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	if p := refreshed(leaguecatalog.Config{RosterPath: stale, RulesPath: repoRules(t), MaxAge: 7 * 24 * time.Hour}, &recLogger{}); len(p.Entities()) != 0 {
		t.Error("a roster older than MaxAge must disable league entities")
	}
	corrupt := filepath.Join(dir, "corrupt.csv")
	if err := os.WriteFile(corrupt, []byte("not,a,roster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := refreshed(leaguecatalog.Config{RosterPath: corrupt, RulesPath: repoRules(t)}, &recLogger{}); len(p.Entities()) != 0 {
		t.Error("a corrupt roster must disable league entities")
	}
	fresh := writeRoster(t, t.TempDir(), now, "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	if p := refreshed(leaguecatalog.Config{RosterPath: fresh, RulesPath: filepath.Join(dir, "nope.yml")}, &recLogger{}); len(p.Entities()) != 0 {
		t.Error("an invalid rules file must disable league entities, not crash")
	}
}

func TestProvider_ReloadsOnlyWhenTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := writeRoster(t, dir, now.Add(-time.Hour), "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	p := refreshed(leaguecatalog.Config{RosterPath: path, RulesPath: repoRules(t)}, &recLogger{})
	first := p.Entities()
	p.Refresh(now.Add(time.Minute))
	if again := p.Entities(); len(again) != len(first) || &again[0] != &first[0] {
		t.Fatal("an unchanged roster must keep the very same rules slice (no rebuild)")
	}
	writeRoster(t, dir, now, "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5", "Quinn Dummy,Quinn,Dummy,Quinn,CAP,QB,ACT,g3,5")
	p.Refresh(now.Add(2 * time.Minute))
	if got := p.Entities(); len(got) != 2 {
		t.Fatalf("Entities() = %d after the roster changed, want 2", len(got))
	}
}

func TestProvider_ConcurrentRefreshAndReadAreSafe(t *testing.T) {
	dir := t.TempDir()
	path := writeRoster(t, dir, now, "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	p := leaguecatalog.New(leaguecatalog.Config{RosterPath: path, RulesPath: repoRules(t)}, baseCatalog(), &recLogger{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				p.Refresh(now)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = p.Entities()
				_ = p.Status()
			}
		}()
	}
	wg.Wait()
}

func TestFromConfig_OffReturnsNil(t *testing.T) {
	if p := leaguecatalog.FromConfig(config.LeagueCatalogConfig{Mode: "off"}, nil, &recLogger{}); p != nil {
		t.Fatal("mode off must not build a provider")
	}
	if p := leaguecatalog.FromConfig(config.LeagueCatalogConfig{}, nil, &recLogger{}); p != nil {
		t.Fatal("the zero value must not build a provider")
	}
	cfg := config.LeagueCatalogConfig{Mode: "mentions_only", RosterPath: "r.csv", RulesPath: repoRules(t), MaxAgeDays: 3}
	if p := leaguecatalog.FromConfig(cfg, nil, &recLogger{}); p == nil {
		t.Fatal("mentions_only must build a provider")
	}
}

func TestProvider_StaleRosterWarnsOnceNotEveryHour(t *testing.T) {
	dir := t.TempDir()
	path := writeRoster(t, dir, now.Add(-30*24*time.Hour), "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	log := &recLogger{}
	p := leaguecatalog.New(leaguecatalog.Config{RosterPath: path, RulesPath: repoRules(t), MaxAge: 7 * 24 * time.Hour}, baseCatalog(), log)
	for h := 0; h < 6; h++ {
		p.Refresh(now.Add(time.Duration(h) * time.Hour))
	}
	if log.warns != 1 {
		t.Fatalf("warns = %d, want one for a roster that stays stale (the age changes every hour, the reason must not)", log.warns)
	}
	if p.Status().Reason == "" {
		t.Fatal("a stale roster must still report a reason")
	}
}

func TestProvider_ReloadsWhenOnlyTheRulesFileChanges(t *testing.T) {
	dir := t.TempDir()
	roster := writeRoster(t, dir, now, "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	rules := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(rules, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := leaguecatalog.New(leaguecatalog.Config{RosterPath: roster, RulesPath: rules}, baseCatalog(), &recLogger{})
	p.Refresh(now)
	if p.Status().Loaded {
		t.Fatal("an invalid rules file must disable the feature")
	}
	good, err := os.ReadFile(repoRules(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, good, 0o600); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Minute)
	if err := os.Chtimes(rules, future, future); err != nil {
		t.Fatal(err)
	}
	p.Refresh(now.Add(2 * time.Minute))
	if !p.Status().Loaded || len(p.Entities()) != 1 {
		t.Fatalf("fixing only the rules file must be picked up without touching the roster: %+v", p.Status())
	}
}

func TestProvider_AFailedBuildIsNotRetriedUntilAFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roster.csv")
	if err := os.WriteFile(path, []byte("not,a,roster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	p := leaguecatalog.New(leaguecatalog.Config{RosterPath: path, RulesPath: repoRules(t)}, baseCatalog(), &recLogger{})
	p.Refresh(now)
	// Replace the content but keep the very same mtime: the provider keys its
	// decision on file times, so it must not re-parse on every cycle.
	writeRoster(t, dir, now, "Avery Placeholder,Avery,Placeholder,Avery,SHE,LB,ACT,g1,5")
	p.Refresh(now)
	if p.Status().Loaded {
		t.Fatal("a failed build must be remembered by file times and not retried every cycle")
	}
}
