// Package configcheck loads and strictly validates the settings file the way
// `config validate` does, for the CLI and the admin action.
package configcheck

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

// Report is the outcome of a successful validation.
type Report struct {
	// Profile is the profile the file was validated for.
	Profile config.Profile
	// Active lists the fetch categories that would run.
	Active []string
	// Inactive maps each skipped fetch category to the reason.
	Inactive map[string]string
	// Config is the loaded settings.
	Config *config.Config
}

// Validate loads the settings file at path, loads the source catalog it
// points at and builds the fetcher for the profile, which applies the
// profile's validation and capability resolution. The fetcher is discarded.
func Validate(path string, profile config.Profile, ext fetcherwiring.Extensions) (Report, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return Report{}, err
	}
	catalog, err := sourcesfile.Load(cfg.Fetch.SourcesPath)
	if err != nil {
		return Report{}, fmt.Errorf("load sources: %w", err)
	}
	_, report, err := fetcherwiring.Build(cfg, catalog, profile, clock.Real{}, discardLogger{}, ext)
	if err != nil {
		return Report{}, err
	}
	return Report{Profile: profile, Active: report.Active(), Inactive: report.Inactive(), Config: cfg}, nil
}

type discardLogger struct{}

func (discardLogger) Debug(string, ...any)        {}
func (discardLogger) Info(string, ...any)         {}
func (discardLogger) Warn(string, ...any)         {}
func (discardLogger) Error(string, error, ...any) {}
