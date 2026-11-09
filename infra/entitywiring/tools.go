package entitywiring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycatalogbuild"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	domainresolution "github.com/Mundo-Dolphins/phins-news-engine/domain/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse/rostersync"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var (
	// ErrNoText is returned by Extract when title, description and body are
	// all blank.
	ErrNoText = errors.New("give at least one of --title, --description, --body or --body-file")
	// ErrFallbackDisabled is the reason the online entity resolution fallback
	// cannot be tested: entity_extraction.fallback.enabled is false.
	ErrFallbackDisabled = errors.New("entity_extraction.fallback.enabled is false")
)

// ExtractInput is the text and the optional path overrides of one Extract
// run. Empty paths fall back to the configuration.
type ExtractInput struct {
	Title, Description, Body             string
	RulesPath, ManualPath, GeneratedPath string
	GeneratedRulesPath                   string
	League                               bool
	RosterPath, LeagueRulesPath          string
}

// NeedsConfig reports whether Extract needs a loaded configuration: some of
// the rules, manual or generated paths is missing, or League is set and the
// roster or league rules path is missing.
func NeedsConfig(in ExtractInput) bool {
	return in.RulesPath == "" || in.ManualPath == "" || in.GeneratedPath == "" ||
		(in.League && (in.RosterPath == "" || in.LeagueRulesPath == ""))
}

// Extract runs the production deterministic extractor on the text of in with
// the configured (or overridden) rules and entity catalog. It is read-only.
// cfg may be nil when NeedsConfig(in) is false. It returns the generated
// event-rules warnings for the caller to show. log receives the league
// provider's messages. Errors carry no command prefix.
func Extract(cfg *config.Config, in ExtractInput, now time.Time, log out.Logger) (eventsignals.Representation, []string, error) {
	if strings.TrimSpace(in.Title+in.Description+in.Body) == "" {
		return eventsignals.Representation{}, nil, ErrNoText
	}
	if cfg == nil && NeedsConfig(in) {
		return eventsignals.Representation{}, nil, errors.New("configuration required: give --rules, --manual and --generated")
	}
	rulesPath, manualPath, generatedPath := in.RulesPath, in.ManualPath, in.GeneratedPath
	if rulesPath == "" {
		rulesPath = cfg.Clustering.RulesPath
	}
	if manualPath == "" {
		manualPath = cfg.Clustering.EntityCatalog.ManualPath
	}
	if generatedPath == "" {
		generatedPath = cfg.Clustering.EntityCatalog.GeneratedPath
	}
	rulesFile, err := clusteringrules.LoadWithEntityCatalog(rulesPath, manualPath, generatedPath)
	if err != nil {
		return eventsignals.Representation{}, nil, err
	}
	// Merged like engine/run.go (empty path: none).
	generatedRulesPath := in.GeneratedRulesPath
	if generatedRulesPath == "" && cfg != nil {
		generatedRulesPath = cfg.Clustering.GeneratedRulesPath
	}
	rulesFile, warnings, err := clusteringrules.LoadWithGenerated(rulesFile, generatedRulesPath)
	if err != nil {
		return eventsignals.Representation{}, nil, err
	}
	rules := rulesFile.ToEventSignalsRules()

	if in.League {
		rosterPath, leagueRulesPath := in.RosterPath, in.LeagueRulesPath
		if rosterPath == "" {
			rosterPath = cfg.Clustering.EntityCatalog.League.RosterPath
		}
		if rosterPath == "" {
			rosterPath = cfg.EntityDiscovery.Roster.Path
		}
		if leagueRulesPath == "" {
			leagueRulesPath = cfg.Clustering.EntityCatalog.League.RulesPath
		}
		if leagueRulesPath == "" {
			leagueRulesPath = "configs/league_alias_rules.yml"
		}
		provider := leaguecatalog.New(leaguecatalog.Config{RosterPath: rosterPath, RulesPath: leagueRulesPath}, rules.Entities, log)
		provider.Refresh(now)
		if st := provider.Status(); !st.Loaded {
			return eventsignals.Representation{}, nil, fmt.Errorf("league rules unavailable: %s", st.Reason)
		}
		rules.Entities = append(append([]eventsignals.EntityRule(nil), rules.Entities...), provider.Entities()...)
	}

	at := now.UTC()
	rep := eventsignals.Extract(article.Article{
		ID: "adhoc-extract", Title: in.Title, Summary: in.Description, Body: in.Body, PublishedAt: at,
	}, rules, at)
	return rep, warnings, nil
}

// FallbackReport is the outcome of one FallbackTest. When Err is set the
// provider call failed and the other result fields are empty.
type FallbackReport struct {
	// Provider is the configured provider name, or "unknown".
	Provider string
	// Err is the provider call error.
	Err error
	// Result is the raw LLM answer.
	Result out.EntityResolutionResult
	// ValidateErr is the local validation error applied to Result, if any.
	ValidateErr error
}

// NewFallbackResolver builds the online entity resolution client from fb.
func NewFallbackResolver(fb config.EntityFallbackConfig) out.EntityResolutionLLM {
	return llmclient.NewResolver(llmclient.Config{
		BaseURL: fb.BaseURL, APIKey: fb.APIKey, Model: fb.Model,
		Temperature: fb.Temperature, MaxTokens: fb.MaxTokens, Timeout: fb.Timeout(),
	})
}

// FallbackTest sends one candidate through resolver and applies the same
// local validation the online fallback applies in production. It never opens
// the resolution cache: it is a prompt/provider diagnostic.
func FallbackTest(ctx context.Context, resolver out.EntityResolutionLLM, fb config.EntityFallbackConfig, candidate, contextText, field string) FallbackReport {
	normalized := entityproposal.Normalize(candidate)
	result, err := resolver.Resolve(ctx, out.EntityResolutionCandidate{
		Text: candidate, NormalizedText: normalized, Field: field, Context: contextText,
	})
	report := FallbackReport{Provider: fb.Provider}
	if report.Provider == "" {
		report.Provider = "unknown"
	}
	if err != nil {
		report.Err = err
		return report
	}
	report.Result = result
	report.ValidateErr = domainresolution.Validate(
		domainresolution.Candidate{Text: candidate, NormalizedText: normalized, Field: field, Context: contextText},
		domainresolution.Resolution{Resolved: result.Resolved, Type: result.Type, CanonicalName: result.CanonicalName, Confidence: result.Confidence},
		fb.MinConfidence,
	)
	return report
}

// SyncRoster downloads the nflverse roster atomically and validates it. The
// destination is pathOverride, else rc.Path; the URL is urlOverride, else
// rc.URL, else the nflverse default for the year of now. The previous file is
// kept when the download or its validation fails.
func SyncRoster(ctx context.Context, rc config.RosterConfig, urlOverride, pathOverride string, now time.Time) (int, string, error) {
	dest, src := pathOverride, urlOverride
	if dest == "" {
		dest = rc.Path
		if src == "" {
			src = rc.URL
		}
	}
	if dest == "" {
		return 0, "", fmt.Errorf("no roster path (set entity_discovery.roster.path or pass --path)")
	}
	if src == "" {
		src = nflverse.DefaultURL(now.Year())
	}
	var count int
	validate := func(p string) error {
		f, err := os.OpenInRoot(filepath.Dir(p), filepath.Base(p))
		if err != nil {
			return err
		}
		defer f.Close()
		players, err := nflverse.ParseRoster(f)
		if err != nil {
			return err
		}
		if len(players) == 0 {
			return fmt.Errorf("roster file has no players")
		}
		count = len(players)
		return nil
	}
	if err := rostersync.Download(ctx, netguard.NewClient(60*time.Second, 60*time.Second, netguard.AllowPrivate), src, dest, validate); err != nil {
		return 0, "", err
	}
	return count, dest, nil
}

// GenerateCatalog rebuilds the generated half of the entity catalog from the
// team dictionary at dictionaryPath, writes it to outPath and returns how many
// entities it wrote.
func GenerateCatalog(dictionaryPath, outPath string) (int, error) {
	d, err := dictionaryfile.Load(dictionaryPath)
	if err != nil {
		return 0, err
	}
	for _, issue := range dictionary.Validate(d) {
		if issue.Severity == dictionary.SeverityError {
			return 0, fmt.Errorf("%s failed validation: %s", dictionaryPath, issue)
		}
	}
	entitiesList, err := entitycatalogbuild.Build(d)
	if err != nil {
		return 0, err
	}
	if err := entitycatalog.WriteGenerated(outPath, entitiesList); err != nil {
		return 0, err
	}
	return len(entitiesList), nil
}
