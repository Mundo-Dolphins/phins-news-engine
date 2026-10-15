// cli/cmd/events/events_test.go
package events_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	return repofs.Path(t, filepath.FromSlash(rel))
}

// gateFixture returns a fixture config whose clustering block points at the
// repository rules and entity catalog and at a temp generated rules file.
func gateFixture(t *testing.T, generatedBody string) string {
	t.Helper()
	configPath := testsupport.WriteFixture(t)
	generated := filepath.Join(filepath.Dir(configPath), "event_rules_generated.yml")
	if generatedBody != "" {
		if err := os.WriteFile(generated, []byte(generatedBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	const off = "clustering:\n  enabled: false\n"
	s := strings.Replace(string(raw), off, off+fmt.Sprintf("  rules_path: %q\n  generated_rules_path: %q\n  entity_catalog:\n    manual_path: %q\n    generated_path: %q\n",
		repoFile(t, "configs/example/event_clustering_rules.yml"), generated,
		repoFile(t, "configs/example/entity_catalog_manual.yml"), repoFile(t, "configs/example/entity_catalog_generated.yml")), 1)
	if err := os.WriteFile(configPath, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestEventsGate_PrintsBeforeAfterAndVerdict(t *testing.T) {
	configPath := gateFixture(t, "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx unmatched phrase, proposal_id: evr_1, prompt_version: event-rules-v1}\n")
	out, err := testsupport.Execute(t, "--config", configPath, "events", "gate", "--dataset", repoFile(t, "testdata/clustering_groups.yml"))
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	for _, want := range []string{"(1 phrase(s))", "before (hand-written): precision=", "after (with generated): precision=", "gate not exercised (0 dataset articles affected)", "gate: PASS"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestEventsGate_MissingDatasetFails(t *testing.T) {
	configPath := gateFixture(t, "")
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "gate", "--dataset", filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("a missing dataset must fail the read-only gate report")
	}
}

func TestEventsReview_DisabledStepIsAnError(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	_, err := testsupport.ExecuteWithStdin(t, "", "--config", configPath, "events", "review")
	if err == nil || !strings.Contains(err.Error(), "offline_training.llm") {
		t.Fatalf("err = %v, want the disabled reason", err)
	}
}

// seedGeneric stores generic articles with representations; titles[i] gets
// id g<i>.
func seedGeneric(t *testing.T, configPath string, titles ...string) {
	t.Helper()
	seedArticles(t, configPath, "g", eventsignals.EventTypeGeneric, titles...)
}

// seedArticles stores articles whose representation has event type typ;
// titles[i] gets id <prefix><i>.
func seedArticles(t *testing.T, configPath, prefix string, typ eventsignals.EventType, titles ...string) {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(configPath), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	now := time.Now().UTC()
	for i, title := range titles {
		id := fmt.Sprintf("%s%d", prefix, i)
		a := article.Article{
			ID: id, Title: title, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: testsupport.MustID(t, source.TypeRSS, "src"), Type: source.TypeRSS},
			FetchedAt: now, PublishedAt: now.Add(-time.Duration(i) * time.Hour), Status: article.StatusFetched,
		}
		if err := articles.Save(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		rep := eventsignals.Representation{ArticleID: id, SourceID: "src", EventType: typ, PublishedAt: a.PublishedAt, FetchedAt: now}
		if err := clusters.SaveRepresentation(context.Background(), rep, "rules-v1", now); err != nil {
			t.Fatal(err)
		}
	}
}

const impactGenerated = "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: event-rules-v1}\ncontent_cues:\n  rumor:\n    - {phrase: zzqx whispers, proposal_id: evr_2, prompt_version: event-rules-v1}\n"

func TestEventsImpact_ReportsRetypedGenericAndWritesNothing(t *testing.T) {
	configPath := gateFixture(t, impactGenerated)
	seedGeneric(t, configPath, "Isotopes zzqx pens a pact with veteran", "Weather in Springfield is sunny")
	generated := filepath.Join(filepath.Dir(configPath), "event_rules_generated.yml")
	before, _ := os.ReadFile(generated)

	out, err := testsupport.Execute(t, "--config", configPath, "events", "impact")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	for _, want := range []string{"2 generic article(s)", "1 would change", "transaction: 1", "Isotopes zzqx pens a pact with veteran"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Weather in Springfield") {
		t.Errorf("unchanged article listed:\n%s", out)
	}
	after, _ := os.ReadFile(generated)
	if string(before) != string(after) {
		t.Error("impact must not write the generated file")
	}
}

func TestEventsImpact_NoGeneratedFileChangesNothing(t *testing.T) {
	configPath := gateFixture(t, "")
	seedGeneric(t, configPath, "Isotopes zzqx pens a pact with veteran")
	out, err := testsupport.Execute(t, "--config", configPath, "events", "impact", "--days", "7")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	if !strings.Contains(out, "0 would change") || !strings.Contains(out, "generated rules file not found:") || !strings.Contains(out, "nothing to compare") {
		t.Errorf("output:\n%s", out)
	}
}

func seedProposals(t *testing.T, configPath string, ps ...eventruleproposal.Proposal) []string {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(configPath), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqliteeventruleproposal.NewRepository(db)
	var ids []string
	for _, p := range ps {
		p.CreatedAt = time.Now().UTC()
		got, err := repo.Upsert(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != eventruleproposal.StatusPending {
			got.Status = p.Status
			if err := repo.Save(context.Background(), got); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, got.ID)
	}
	return ids
}

func TestEventsBrief_PrintsBriefForTypeProposal(t *testing.T) {
	configPath := gateFixture(t, "")
	seedGeneric(t, configPath, "Isotopes extend Ezo", "Dale gets new deal")
	ids := seedProposals(t, configPath,
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "contract extension", Description: "Re-signings.", EvidenceIDs: []string{"g0", "g1", "missing"}, Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
		eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign", Phrase: "inks a deal", EvidenceIDs: []string{"g0"}, Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
	)
	out, err := testsupport.Execute(t, "--config", configPath, "events", "brief", ids[0])
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	for _, want := range []string{"contract extension", "- Isotopes extend Ezo", "- Dale gets new deal", "ActionContractExtension", "knownEventTypes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "brief", ids[1]); err == nil || !strings.Contains(err.Error(), "not a type proposal") {
		t.Errorf("phrase proposal err = %v", err)
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "brief", "evr_nope"); err == nil {
		t.Error("unknown proposal must fail")
	}
}

func TestEventsBrief_RejectedProposalFails(t *testing.T) {
	configPath := gateFixture(t, "")
	ids := seedProposals(t, configPath, eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "x type", Status: eventruleproposal.StatusRejected, PromptVersion: "v1"})
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "brief", ids[0]); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v", err)
	}
}

func TestEventsImpact_ReportsTypedArticlesThatWouldChangeType(t *testing.T) {
	configPath := gateFixture(t, impactGenerated)
	seedGeneric(t, configPath, "Weather in Springfield is sunny")
	seedArticles(t, configPath, "t", eventsignals.EventTypeTransaction, "Isotopes sign veteran linebacker amid zzqx whispers", "Isotopes sign veteran kicker")
	out, err := testsupport.Execute(t, "--config", configPath, "events", "impact")
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	for _, want := range []string{"2 typed article(s), 1 would change type", "transaction -> rumor: 1", "Isotopes sign veteran linebacker amid zzqx whispers"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "veteran kicker") {
		t.Errorf("unchanged typed article listed:\n%s", out)
	}
}

func TestEventsType_ListApproveReject(t *testing.T) {
	configPath := gateFixture(t, "")
	seedGeneric(t, configPath, "Isotopes extend Ezo")
	ids := seedProposals(t, configPath,
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "contract extension", Description: "Re-signings.", EvidenceIDs: []string{"g0"}, Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "stadium works", Description: "Construction.", Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
		eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign", Phrase: "inks a deal", EvidenceIDs: []string{"g0"}, Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "old idea", Status: eventruleproposal.StatusRejected, PromptVersion: "v1"},
	)
	ext, works, phrase, rejected := ids[0], ids[1], ids[2], ids[3]

	out, err := testsupport.Execute(t, "--config", configPath, "events", "type", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{ext, "contract extension", works, "stadium works"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, phrase) || strings.Contains(out, "old idea") {
		t.Errorf("list shows a phrase or a rejected proposal:\n%s", out)
	}

	if out, err := testsupport.Execute(t, "--config", configPath, "events", "type", "approve", ext); err != nil || !strings.Contains(out, "approved") {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if out, err := testsupport.Execute(t, "--config", configPath, "events", "type", "reject", works); err != nil || !strings.Contains(out, "rejected") {
		t.Fatalf("reject: %v\n%s", err, out)
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "type", "approve", phrase); err == nil || !strings.Contains(err.Error(), "not a type proposal") {
		t.Errorf("approving a phrase proposal: err = %v", err)
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "type", "approve", rejected); err == nil || !strings.Contains(err.Error(), "cannot move") {
		t.Errorf("approving a rejected proposal: err = %v", err)
	}
	if _, err := testsupport.Execute(t, "--config", configPath, "events", "type", "reject", "evr_nope"); err == nil {
		t.Error("unknown proposal must fail")
	}

	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(configPath), "operational.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqliteeventruleproposal.NewRepository(db)
	for id, want := range map[string]eventruleproposal.Status{ext: eventruleproposal.StatusApproved, works: eventruleproposal.StatusRejected, phrase: eventruleproposal.StatusPending} {
		p, err := repo.Get(context.Background(), id)
		if err != nil || p.Status != want {
			t.Errorf("%s: status %s err %v, want %s", id, p.Status, err, want)
		}
	}
	if out, err := testsupport.Execute(t, "--config", configPath, "events", "brief", ext); err != nil || !strings.Contains(out, "contract extension") {
		t.Errorf("brief of an approved type proposal: %v\n%s", err, out)
	}
}
