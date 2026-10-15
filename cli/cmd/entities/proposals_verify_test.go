package entities

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

func appendToFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func proposalsVerifyArgs(settingsPath string, extra ...string) []string {
	dir := filepath.Dir(settingsPath)
	return append([]string{
		"--manual", filepath.Join(dir, "manual.yml"),
		"--generated", filepath.Join(dir, "generated.yml"),
	}, extra...)
}

func TestEntitiesCommand_RegistersProposalsVerify(t *testing.T) {
	cmd, _, err := NewCommand().Find([]string{"proposals", "verify"})
	if err != nil || cmd == nil || cmd.Name() != "verify" {
		t.Fatalf("Find(proposals verify) = %v, %v", cmd, err)
	}
}

func TestProposalsVerifyCommand_RequiresValidWebLookupConfig(t *testing.T) {
	dbPath, _ := newDiscoverTestDB(t)
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, "http://example.invalid")
	appendToFile(t, settingsPath, "catalog_maintenance:\n  web_lookup:\n    enabled: false\n    provider: searxng\n")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newProposalsVerifyCommand()
	cmd.SetArgs(proposalsVerifyArgs(settingsPath))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "base_url is required when enabled") {
		t.Fatalf("Execute() error = %v, want web_lookup validation failure", err)
	}
}

func TestProposalsVerifyCommand_DryRunReportsAndWritesNothing(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	id, err := repo.Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Springfield", NormalizedName: "springfield",
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity,
		Confidence: 0.6, Occurrences: 1, ExtractorVersion: "test", CreatedAt: time.Now().UTC(),
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "Springfield wins"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both the LLM and the search provider fail: the verifier must report
	// uncertain and the command must leave the proposal untouched.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, server.URL)
	appendToFile(t, settingsPath, "catalog_maintenance:\n  web_lookup:\n    enabled: true\n    provider: searxng\n    base_url: "+server.URL+
		"\n    timeout_seconds: 2\n    max_searches_per_candidate: 2\n    max_results_per_search: 3\n    max_searches_per_run: 10\n    cache_ttl: 1h\n    trusted_domains: [\"league.example.com\"]\n")
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	cmd := newProposalsVerifyCommand()
	cmd.SetArgs(proposalsVerifyArgs(settingsPath, "--dry-run", "--limit", "5"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out.String())
	}
	for _, want := range []string{"dry run", "processed: 1", "uncertain (left pending): 1", "skipped (already verified/attempted): 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	p, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != entityproposal.StatusPending {
		t.Fatalf("status = %s, want pending", p.Status)
	}
}

type namedVerifier map[string]entitydiscovery.VerifyResult

func (n namedVerifier) Verify(_ context.Context, c entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	return n[c.Name]
}

// The command builds its verifier internally, so Keep/Reject persistence is
// exercised at the ReverifyPending level against the real sqlite repo.
func TestReverifyPending_RealSQLite_KeepAndRejectPersist(t *testing.T) {
	_, db := newDiscoverTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	ids := map[string]string{}
	for _, n := range []string{"Patrick Ostrander", "Springfield Mackerels", "Springfield"} {
		id, err := repo.Upsert(ctx, entityproposal.Proposal{
			CanonicalName: n, NormalizedName: entityproposal.Normalize(n),
			EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
			Confidence: 0.6, Occurrences: 1, ExtractorVersion: "t", CreatedAt: time.Now().UTC(),
			Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: n}},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = id
	}
	web := []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/1", Title: "t"}}
	v := namedVerifier{
		"Patrick Ostrander":     {Disposition: weblookup.DispositionKeep, Resolved: true, Confidence: 0.95, WebSources: web, Metadata: map[string]string{"league": "NFL"}},
		"Springfield":           {Disposition: weblookup.DispositionUncertain},
		"Springfield Mackerels": {Disposition: weblookup.DispositionReject, Resolved: true, Reason: "MLB", WebSources: web, Metadata: map[string]string{"league": "MLB"}},
	}
	res, err := entitydiscovery.ReverifyPending(ctx, repo, v, entitydiscovery.BuildCatalogIndex(nil), entitydiscovery.ReverifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != 1 || res.AutoRejected != 1 || res.Uncertain != 1 {
		t.Fatalf("res = %+v", res)
	}
	k, _ := repo.Get(ctx, ids["Patrick Ostrander"])
	if k.Status != entityproposal.StatusPending || k.Metadata["league"] != "NFL" || len(k.WebSources) != 1 || k.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] == "" {
		t.Fatalf("kept = %+v", k)
	}
	r, _ := repo.Get(ctx, ids["Springfield Mackerels"])
	if r.Status != entityproposal.StatusRejected || r.DecisionReason != entityproposal.ReasonAutoRejectedNonNFL || r.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] == "" {
		t.Fatalf("rejected = %+v", r)
	}
	u, _ := repo.Get(ctx, ids["Springfield"])
	if u.Status != entityproposal.StatusPending || u.Metadata[entitydiscovery.MetadataVerifyAttemptedAt] == "" {
		t.Fatalf("uncertain = %+v", u)
	}
}

func TestProposalsVerifyCommand_RejectsUnknownType(t *testing.T) {
	cmd := newProposalsVerifyCommand()
	cmd.SetArgs([]string{"--type", "mascot"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--type") {
		t.Fatalf("Execute() error = %v, want --type validation error", err)
	}
}

func TestApplyProviderFlag(t *testing.T) {
	var f verificationFlags
	if err := applyProviderFlag("", &f); err != nil || f != (verificationFlags{}) {
		t.Fatalf("empty: %+v %v", f, err)
	}
	if err := applyProviderFlag("wikidata", &f); err != nil || !f.wikidata || !f.noFallback {
		t.Fatalf("wikidata: %+v %v", f, err)
	}
	for _, bad := range []verificationFlags{{noWikidata: true}, {fallback: true}, {offline: true}} {
		bad := bad
		if err := applyProviderFlag("wikidata", &bad); err == nil {
			t.Errorf("want conflict error for %+v", bad)
		}
	}
	if err := applyProviderFlag("llm", &verificationFlags{}); err == nil {
		t.Error("want error for unknown provider")
	}
}

func TestProposalsVerifyCommand_RejectsUnknownProvider(t *testing.T) {
	cmd := newProposalsVerifyCommand()
	cmd.SetArgs([]string{"--provider", "llm"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--provider") {
		t.Fatalf("err = %v", err)
	}
}
