// cli/cmd/editorial/editorialcycle_eventrules_test.go
package editorial_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	toolsevents "github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/events"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

type fakeEventRuleLLM struct {
	phrases []appout.EventPhraseProposal
	err     error
	calls   int
}

func (f *fakeEventRuleLLM) DiscoverTopics(context.Context, []appout.EventRuleArticle) ([]appout.EventTopicProposal, error) {
	f.calls++
	return nil, f.err
}

func (f *fakeEventRuleLLM) DiscoverPhrases(context.Context, []appout.EventRuleArticle, []string, []string) ([]appout.EventPhraseProposal, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.phrases, nil
}

func useFakeEventRuleLLM(t *testing.T, f *fakeEventRuleLLM) {
	t.Helper()
	old := toolsevents.NewCycleLLM
	toolsevents.NewCycleLLM = func(*cobra.Command, config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM { return f }
	t.Cleanup(func() { toolsevents.NewCycleLLM = old })
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	return repofs.Path(t, filepath.FromSlash(rel))
}

// eventRulesFixture returns a fixture config with the event rules step
// configured: the repository's rules and entity catalog (read-only), the
// offline LLM and a generated rules path in the temp dir.
func eventRulesFixture(t *testing.T) (configPath, generated string) {
	t.Helper()
	configPath = testsupport.WriteFixture(t)
	generated = filepath.Join(filepath.Dir(configPath), "event_rules_generated.yml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	const off = "clustering:\n  enabled: false\n"
	if !strings.Contains(s, off) {
		t.Fatal("fixture no longer has the clustering block this helper patches")
	}
	s = strings.Replace(s, off, off+fmt.Sprintf("  rules_path: %q\n  generated_rules_path: %q\n  entity_catalog:\n    manual_path: %q\n    generated_path: %q\n",
		repoFile(t, "configs/example/event_clustering_rules.yml"), generated,
		repoFile(t, "configs/example/entity_catalog_manual.yml"), repoFile(t, "configs/example/entity_catalog_generated.yml")), 1)
	const llm = "offline_training:\n  llm:\n"
	if !strings.Contains(s, llm) {
		t.Fatal("fixture no longer has the offline_training.llm block this helper patches")
	}
	s = strings.Replace(s, llm, llm+"    base_url: http://127.0.0.1:1/v1\n    model: test\n", 1)
	if err := os.WriteFile(configPath, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, generated
}

func markGeneric(t *testing.T, dbPath, articleID string) {
	t.Helper()
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if err := sqliteadapter.NewClusterRepository(db).SaveRepresentation(context.Background(), eventsignals.Representation{
		ArticleID: articleID, EventType: eventsignals.EventTypeGeneric, PublishedAt: now, FetchedAt: now,
	}, "rules-v1", now); err != nil {
		t.Fatal(err)
	}
}

func eventRuleArgs(t *testing.T, configPath string, extra ...string) []string {
	t.Helper()
	args := append([]string{"--config", configPath, "editorial", "cycle"}, baseCycleArgs...)
	args = append(args, "--skip-dictionary", "--skip-entities", "--event-rules-dataset", repofs.Path(t, "testdata", "clustering_groups.yml"))
	return append(args, extra...)
}

func TestEditorialCycleCmd_EventRulesStep_ApprovesAndAppliesAPhrase(t *testing.T) {
	configPath, generated := eventRulesFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "g1", "Veteran puts pen to paper in Springfield")
	markGeneric(t, dbPath, "g1")
	llm := &fakeEventRuleLLM{phrases: []appout.EventPhraseProposal{
		{TargetKind: "action", Target: "sign", Phrase: "puts pen to paper", ArticleIDs: []string{"g1"}},
	}}
	useFakeEventRuleLLM(t, llm)

	out, err := testsupport.ExecuteWithStdin(t, "p\na\n", eventRuleArgs(t, configPath)...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	r := readReport(t, out)
	if r.EventRulesSkipped || r.EventRulesSampled != 1 || r.EventRulesProposed != 1 || r.EventRulesApproved != 1 || r.EventRulesApplied != 1 || r.EventRulesGateBlocked || r.EventRulesDiscoveryFailed {
		t.Fatalf("report = %+v\n%s", r, out)
	}
	g, err := clusteringrules.ParseGenerated(generated)
	if err != nil {
		t.Fatal(err)
	}
	if es := g.ActionTerms["sign"]; len(es) != 1 || es[0].Phrase != "puts pen to paper" {
		t.Fatalf("generated rules = %+v; the approved phrase must be written", g)
	}
	if !strings.Contains(out, "Event rules: sampled=1 proposed=1 approved=1 applied=1") {
		t.Fatalf("summary lacks the event rules line:\n%s", out)
	}
	// The phrase appears in no gold dataset article, so the gate passed
	// without being exercised; the audit must say so.
	if !r.EventRulesGateNotExercised || !strings.Contains(out, "gate not exercised (0 dataset articles affected)") {
		t.Fatalf("report = %+v; a vacuous gate must be audited\n%s", r, out)
	}
}

func TestEditorialCycleCmd_SkipEventRules_NeverCallsTheLLM(t *testing.T) {
	configPath, generated := eventRulesFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "g2", "Veteran puts pen to paper in Springfield")
	markGeneric(t, dbPath, "g2")
	llm := &fakeEventRuleLLM{}
	useFakeEventRuleLLM(t, llm)

	out, err := testsupport.ExecuteWithStdin(t, "p\n", eventRuleArgs(t, configPath, "--skip-event-rules")...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if llm.calls != 0 || strings.Contains(out, "event rules:") {
		t.Fatalf("llm calls=%d; --skip-event-rules must not run or announce the step:\n%s", llm.calls, out)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("--skip-event-rules must not write %s (stat err %v)", generated, err)
	}
	r := readReport(t, out)
	if !r.EventRulesSkipped || r.EventRulesSkipReason != "explicit --skip-event-rules flag" {
		t.Fatalf("report = %+v", r)
	}
}

func TestEditorialCycleCmd_EventRulesStepDisabledByConfig_PrintsOneLine(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "g3")
	llm := &fakeEventRuleLLM{}
	useFakeEventRuleLLM(t, llm)
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-dictionary", "--skip-entities")...)
	out, err := testsupport.ExecuteWithStdin(t, "p\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "event rules: step disabled (offline_training.llm.base_url and model are not configured)") || llm.calls != 0 {
		t.Fatalf("llm calls=%d; output lacks the disabled-step line:\n%s", llm.calls, out)
	}
	if r := readReport(t, out); !r.EventRulesSkipped || !strings.Contains(r.EventRulesSkipReason, "offline_training.llm") {
		t.Fatalf("report = %+v", r)
	}
}

func TestEditorialCycleCmd_EventRulesStep_LLMDownContinues(t *testing.T) {
	configPath, _ := eventRulesFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "g4", "Veteran puts pen to paper in Springfield")
	markGeneric(t, dbPath, "g4")
	useFakeEventRuleLLM(t, &fakeEventRuleLLM{err: fmt.Errorf("connection refused")})
	out, err := testsupport.ExecuteWithStdin(t, "p\n", eventRuleArgs(t, configPath)...)
	if err != nil {
		t.Fatalf("an LLM outage must not fail the cycle: %v\n%s", err, out)
	}
	if r := readReport(t, out); !r.EventRulesDiscoveryFailed || !strings.Contains(out, "WARNING: event rule discovery is unavailable") {
		t.Fatalf("report=%+v\n%s", r, out)
	}
}

func TestEditorialCycleCmd_EventRulesStep_MissingDatasetWarnsAndAppliesNothing(t *testing.T) {
	configPath, generated := eventRulesFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedTitled(t, dbPath, "g5", "Veteran puts pen to paper in Springfield")
	markGeneric(t, dbPath, "g5")
	useFakeEventRuleLLM(t, &fakeEventRuleLLM{phrases: []appout.EventPhraseProposal{
		{TargetKind: "action", Target: "sign", Phrase: "puts pen to paper", ArticleIDs: []string{"g5"}},
	}})
	args := append([]string{"--config", configPath, "editorial", "cycle"}, baseCycleArgs...)
	args = append(args, "--skip-dictionary", "--skip-entities", "--event-rules-dataset", filepath.Join(t.TempDir(), "missing.yml"))
	out, err := testsupport.ExecuteWithStdin(t, "p\na\n", args...)
	if err != nil {
		t.Fatalf("a missing gate dataset must not fail the cycle: %v\n%s", err, out)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("nothing may be applied without the gate (stat err %v)", err)
	}
	r := readReport(t, out)
	if r.EventRulesApplied != 0 || r.EventRulesApproved != 1 || !r.EventRulesGateBlocked || !strings.Contains(out, "WARNING") {
		t.Fatalf("report=%+v\n%s", r, out)
	}
	// The operator is warned before approving, not only after.
	pre, prompt := strings.Index(out, "the clustering gate dataset is missing"), strings.Index(out, "Action:")
	if pre < 0 || prompt < 0 || pre > prompt {
		t.Fatalf("no pre-flight warning before the review (warning at %d, first prompt at %d):\n%s", pre, prompt, out)
	}
}

func TestEditorialCycleCmd_DryRun_ReportsTheEventRulesStep(t *testing.T) {
	configPath, generated := eventRulesFixture(t)
	llm := &fakeEventRuleLLM{}
	useFakeEventRuleLLM(t, llm)
	args := append([]string{"--config", configPath, "editorial", "cycle", "--dry-run"}, baseCycleArgs...)
	out, err := testsupport.Execute(t, args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry run: event rules: step would run") || llm.calls != 0 {
		t.Fatalf("llm calls=%d out=%s", llm.calls, out)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("a dry run must not write %s", generated)
	}

	off := testsupport.WriteFixture(t)
	out, err = testsupport.Execute(t, append([]string{"--config", off, "editorial", "cycle", "--dry-run"}, baseCycleArgs...)...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry run: event rules: step off (") {
		t.Fatalf("dry run must name why the step is off:\n%s", out)
	}
}
