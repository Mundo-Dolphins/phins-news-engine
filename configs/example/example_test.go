// Package example_test proves that every file in configs/example loads
// and validates through the same loaders the engine uses at startup.
// `make validate-config` runs this test.
package example_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryminingfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestExampleConfigs_LoadAndValidate(t *testing.T) {
	// settings.yml enables web lookup, whose key is read from the
	// environment only.
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	cfg, err := config.Load(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatalf("config.Load(settings.yml) error = %v", err)
	}

	// Every path settings.yml names under configs/example must load.
	sources, err := sourcesfile.Load(repofs.Path(t, cfg.Fetch.SourcesPath))
	if err != nil {
		t.Fatalf("sourcesfile.Load error = %v", err)
	}
	policies, err := sourcepolicyfile.Load(repofs.Path(t, cfg.SourcePolicy.Path))
	if err != nil {
		t.Fatalf("sourcepolicyfile.Load error = %v", err)
	}
	if res := usecase.ValidateSourcePolicies(sources, policies); len(res.Issues) > 0 {
		t.Errorf("source policies do not match sources.yml: %v", res.Issues)
	}

	d, err := dictionaryfile.Load(repofs.Path(t, cfg.Dictionary.Path))
	if err != nil {
		t.Fatalf("dictionaryfile.Load error = %v", err)
	}
	for _, issue := range dictionary.Validate(d) {
		if issue.Severity == dictionary.SeverityError {
			t.Errorf("example dictionary: %s", issue)
		}
	}
	if _, err := dictionary.Prepare(d); err != nil {
		t.Errorf("dictionary.Prepare error = %v", err)
	}

	rules, err := clusteringrules.LoadWithEntityCatalog(
		repofs.Path(t, cfg.Clustering.RulesPath),
		repofs.Path(t, cfg.Clustering.EntityCatalog.ManualPath),
		repofs.Path(t, cfg.Clustering.EntityCatalog.GeneratedPath),
	)
	if err != nil {
		t.Fatalf("clusteringrules.LoadWithEntityCatalog error = %v", err)
	}
	if len(rules.Entities) == 0 {
		t.Error("example entity catalog is empty")
	}
	if _, err := entitycatalog.Load(
		repofs.Path(t, cfg.Clustering.EntityCatalog.ManualPath),
		repofs.Path(t, cfg.Clustering.EntityCatalog.GeneratedPath),
	); err != nil {
		t.Errorf("entitycatalog.Load error = %v", err)
	}

	league, err := leaguerules.Load(repofs.Path(t, cfg.Clustering.EntityCatalog.League.RulesPath))
	if err != nil {
		t.Fatalf("leaguerules.Load error = %v", err)
	}
	if _, err := leaguegazetteer.New(nil, league); err != nil {
		t.Errorf("leaguegazetteer.New(example rules) error = %v", err)
	}

	if _, err := dictionaryminingfile.Load(repofs.Path(t, cfg.DictionaryMining.Path)); err != nil {
		t.Errorf("dictionaryminingfile.Load error = %v", err)
	}
	if _, err := editorialveto.Load(repofs.Path(t, cfg.Editorial.VetoPath)); err != nil {
		t.Errorf("editorialveto.Load error = %v", err)
	}
}
