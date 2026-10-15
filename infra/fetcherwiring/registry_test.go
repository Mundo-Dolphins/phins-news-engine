package fetcherwiring

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/compositefetcher"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

// minimalConfigWithCategoryEnabled mirrors baseConfig in fetcherwiring_test.go
// (external test package) with one category enabled.
func minimalConfigWithCategoryEnabled(t *testing.T, category string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Telegram: config.TelegramConfig{ChannelID: "-1001234567890", BotToken: "test-token"},
		Fetch:    config.FetchConfig{GlobalConcurrency: 4, FanInBuffer: 16, SourcesPath: "unused-in-tests"},
		HTTP:     config.HTTPConfig{TimeoutSeconds: 15, MaxResponseBytes: 10 << 20},
	}
	on := config.SourceCategoryConfig{Enabled: true}
	switch category {
	case "website":
		cfg.FetchSources.Website = on
	case "twitter":
		cfg.FetchSources.Twitter = on
	case "instagram":
		cfg.FetchSources.Instagram = on
	default:
		t.Fatalf("unsupported category %q", category)
	}
	return cfg
}

func TestOrderIsStable(t *testing.T) {
	want := "website,rss,reddit,bluesky,twitter,instagram,youtube"
	if got := strings.Join(Order, ","); got != want {
		t.Fatalf("Order = %s, want %s", got, want)
	}
}

func TestUnregisteredExtensionCategoryIsReportedNotSilent(t *testing.T) {
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	_, report, err := Build(cfg, &sourcesfile.Sources{}, config.ProfileLocal, nil, nopLogger{}, Extensions{})
	if err == nil {
		t.Fatal("expected error: no active sources")
	}
	got := report.Inactive()["website"]
	if !strings.Contains(got, "no builder registered") {
		t.Fatalf("website reason = %q, want it to mention 'no builder registered'", got)
	}
}

func TestExtensionBuilderIsUsedAndKeepsOrder(t *testing.T) {
	called := false
	ext := Extensions{Categories: map[string]CategoryBuilder{
		"website": func(*config.Config, *sourcesfile.Sources, config.Profile, out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
			called = true
			return nil, CapabilityEntry{Category: "website", Reason: "stub"}, false
		},
	}}
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	_, report, _ := Build(cfg, &sourcesfile.Sources{}, config.ProfileLocal, nil, nopLogger{}, ext)
	if !called {
		t.Fatal("extension builder not called")
	}
	if report.Entries[0].Category != "website" {
		t.Fatalf("first report entry = %q, want website", report.Entries[0].Category)
	}
}

func TestUnregisteredEnabledCategoryIsFatalUnderFatalProfile(t *testing.T) {
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	_, _, err := Build(cfg, &sourcesfile.Sources{}, config.ProfileTest, nil, nopLogger{}, Extensions{})
	if err == nil || !strings.Contains(err.Error(), "no builder registered") {
		t.Fatalf("err = %v, want it to contain 'no builder registered'", err)
	}
}

func TestNilExtensionBuilderIsTreatedAsUnregistered(t *testing.T) {
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	ext := Extensions{Categories: map[string]CategoryBuilder{"website": nil}}
	_, report, err := Build(cfg, &sourcesfile.Sources{}, config.ProfileLocal, nil, nopLogger{}, ext)
	if err == nil {
		t.Fatal("expected error: no active sources")
	}
	if got := report.Inactive()["website"]; !strings.Contains(got, "no builder registered") {
		t.Fatalf("website reason = %q, want 'no builder registered'", got)
	}
}

func TestUnknownExtensionCategoryIsRejected(t *testing.T) {
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	ext := Extensions{Categories: map[string]CategoryBuilder{"websit": nil}}
	_, _, err := Build(cfg, &sourcesfile.Sources{}, config.ProfileLocal, nil, nopLogger{}, ext)
	if err == nil || !strings.Contains(err.Error(), `unknown extension category "websit"`) {
		t.Fatalf("err = %v, want unknown extension category error", err)
	}
}

func TestReportCategoriesEqualOrder(t *testing.T) {
	ext := Extensions{Categories: map[string]CategoryBuilder{
		"website": func(*config.Config, *sourcesfile.Sources, config.Profile, out.Logger) ([]compositefetcher.Source, CapabilityEntry, bool) {
			return nil, CapabilityEntry{Category: "website", Reason: "stub"}, false
		},
	}}
	cfg := minimalConfigWithCategoryEnabled(t, "website")
	_, report, _ := Build(cfg, &sourcesfile.Sources{}, config.ProfileLocal, nil, nopLogger{}, ext)
	var got []string
	for _, e := range report.Entries {
		got = append(got, e.Category)
	}
	if strings.Join(got, ",") != strings.Join(Order, ",") {
		t.Fatalf("report categories = %v, want %v", got, Order)
	}
}
