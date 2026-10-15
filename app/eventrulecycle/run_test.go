// app/eventrulecycle/run_test.go
package eventrulecycle_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-eventrulecycle-test-template-")
	if err != nil {
		panic("create SQLite test template directory: " + err.Error())
	}
	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		_ = os.RemoveAll(templateDir)
		panic("open SQLite test template: " + err.Error())
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("migrate SQLite test template: " + err.Error())
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("checkpoint SQLite test template: " + err.Error())
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(templateDir)
		panic("close SQLite test template: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(code)
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	src, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type fakeLLM struct {
	topics     []appout.EventTopicProposal
	phrases    []appout.EventPhraseProposal
	topicErr   error
	phraseErr  error
	topicCalls int
	batchSizes []int
	actions    []string
	signals    []string
}

func (f *fakeLLM) DiscoverTopics(_ context.Context, arts []appout.EventRuleArticle) ([]appout.EventTopicProposal, error) {
	f.topicCalls++
	if f.topicErr != nil {
		return nil, f.topicErr
	}
	if f.topicCalls > 1 {
		return nil, nil // report each topic once
	}
	return f.topics, nil
}

func (f *fakeLLM) DiscoverPhrases(_ context.Context, arts []appout.EventRuleArticle, actions, signals []string) ([]appout.EventPhraseProposal, error) {
	f.batchSizes = append(f.batchSizes, len(arts))
	f.actions, f.signals = actions, signals
	if f.phraseErr != nil {
		return nil, f.phraseErr
	}
	if len(f.batchSizes) > 1 {
		return nil, nil
	}
	return f.phrases, nil
}

type harness struct {
	deps     eventrulecycle.Deps
	repo     *sqliteeventruleproposal.Repository
	llm      *fakeLLM
	existing []eventrulecycle.AppliedRule
	written  [][]eventrulecycle.AppliedRule
	gateIn   [][]eventrulecycle.AppliedRule
	gate     eventrulecycle.GateResult
}

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func testSample() []eventruleproposal.Sample {
	s := []eventruleproposal.Sample{
		{Article: article.Article{ID: "a1", Title: "Isotopes inks a deal with veteran", Summary: "Contract news."}, ClusterID: "c1"},
		{Article: article.Article{ID: "a2", Title: "Ezo lands on the shelf with ankle", Summary: "Bad news."}, ClusterID: "c2"},
	}
	for i := 3; i <= 10; i++ {
		s = append(s, eventruleproposal.Sample{Article: article.Article{ID: fmt.Sprintf("a%d", i), Title: fmt.Sprintf("Weather in Springfield %d", i)}, ClusterID: fmt.Sprintf("c%d", i)})
	}
	return s
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		repo: sqliteeventruleproposal.NewRepository(newTestDB(t)),
		llm: &fakeLLM{
			topics: []appout.EventTopicProposal{{Name: "Stadium Renovation", Description: "work on the stadium", ArticleIDs: []string{"a3"}}},
			phrases: []appout.EventPhraseProposal{
				{TargetKind: "action", Target: "sign", Phrase: "  Inks a Deal ", ArticleIDs: []string{"a1", "a3", "zz"}},
				{TargetKind: "action", Target: "injure", Phrase: "lands on the shelf", ArticleIDs: []string{"a2"}},
				{TargetKind: "action", Target: "sign", Phrase: "inks", ArticleIDs: []string{"a1"}},            // fails Check
				{TargetKind: "action", Target: "teleport", Phrase: "inks a deal", ArticleIDs: []string{"a1"}}, // unknown target
				{TargetKind: "mood", Target: "sign", Phrase: "inks a deal", ArticleIDs: []string{"a1"}},       // unknown target kind
				{TargetKind: "signal", Target: "rumor", Phrase: "inks a deal", ArticleIDs: []string{"a3"}},    // not in cited article
			},
		},
		existing: []eventrulecycle.AppliedRule{{TargetKind: "action", Target: "trade", Phrase: "ships off", ProposalID: "evr_old", PromptVersion: "event-rules-v1"}},
		gate:     eventrulecycle.GateResult{OK: true},
	}
	h.deps = eventrulecycle.Deps{
		Sampler:   func(context.Context) ([]eventruleproposal.Sample, error) { return testSample(), nil },
		LLM:       h.llm,
		Proposals: h.repo,
		BaseRules: func() (eventsignals.Rules, error) { return eventsignals.Rules{}, nil },
		Gate: func(_ context.Context, c []eventrulecycle.AppliedRule) (eventrulecycle.GateResult, error) {
			h.gateIn = append(h.gateIn, c)
			return h.gate, nil
		},
		ReadApplied: func(path string) ([]eventrulecycle.AppliedRule, error) {
			if path != "generated.yaml" {
				t.Errorf("ReadApplied path %q", path)
			}
			return append([]eventrulecycle.AppliedRule(nil), h.existing...), nil
		},
		WriteApplied: func(path string, rules []eventrulecycle.AppliedRule) error {
			if path != "generated.yaml" {
				t.Errorf("WriteApplied path %q", path)
			}
			h.written = append(h.written, rules)
			h.existing = append([]eventrulecycle.AppliedRule(nil), rules...)
			return nil
		},
		GeneratedPath: "generated.yaml",
		Now:           func() time.Time { return testNow },
	}
	return h
}

func (h *harness) runWith(sc *bufio.Scanner) (eventrulecycle.Result, string, error) {
	var out bytes.Buffer
	res, err := eventrulecycle.Run(context.Background(), h.deps, eventrulecycle.Input{
		BatchSize:    4,
		CheckOptions: eventruleproposal.DefaultCheckOptions(),
		Scanner:      sc,
		Out:          &out,
	})
	return res, out.String(), err
}

func (h *harness) run(t *testing.T, script string) (eventrulecycle.Result, string) {
	t.Helper()
	res, out, err := h.runWith(bufio.NewScanner(strings.NewReader(script)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	return res, out
}

func phrasesOf(rules []eventrulecycle.AppliedRule) string {
	var ps []string
	for _, r := range rules {
		ps = append(ps, r.Phrase)
	}
	return strings.Join(ps, "|")
}

func (h *harness) byPhrase(t *testing.T) map[string]eventruleproposal.Proposal {
	t.Helper()
	out := map[string]eventruleproposal.Proposal{}
	for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved, eventruleproposal.StatusRejected, eventruleproposal.StatusApplied} {
		ps, err := h.repo.ListByStatus(context.Background(), st)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			key := p.Phrase
			if p.Kind == eventruleproposal.KindType {
				key = "type:" + p.TypeName
			}
			out[key] = p
		}
	}
	return out
}

// Review order is by target kind, target, phrase: injure/"lands on the
// shelf", then sign/"inks" (auto-rejected), then sign/"inks a deal".
func TestRun_ApproveOneRejectOneAndApply(t *testing.T) {
	h := newHarness(t)
	res, out := h.run(t, "a\nr\n")

	if res.Sampled != 10 || res.Proposed != 4 || res.Approved != 1 || res.Rejected != 2 || res.Applied != 1 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if res.DiscoveryFailed || res.Paused || res.GateBlocked {
		t.Fatalf("flags %+v", res)
	}
	if got := h.llm.batchSizes; len(got) != 3 || got[0] != 4 || got[2] != 2 {
		t.Errorf("batch sizes %v", got)
	}
	if len(h.llm.actions) != 26 || len(h.llm.signals) != 6 {
		t.Errorf("allowed lists %d/%d", len(h.llm.actions), len(h.llm.signals))
	}

	ps := h.byPhrase(t)
	if len(ps) != 4 {
		t.Fatalf("stored %v", ps)
	}
	if p := ps["lands on the shelf"]; p.Status != eventruleproposal.StatusApplied || p.PromptVersion != eventrulecycle.EventRulesPromptVersion {
		t.Errorf("applied proposal %+v", p)
	}
	if p := ps["inks a deal"]; p.Status != eventruleproposal.StatusRejected || strings.Join(p.EvidenceIDs, ",") != "a1" {
		t.Errorf("rejected proposal %+v", p)
	}
	if p := ps["inks"]; p.Status != eventruleproposal.StatusRejected {
		t.Errorf("auto-rejected proposal %+v", p)
	}
	if p := ps["type:stadium renovation"]; p.Status != eventruleproposal.StatusPending || p.Kind != eventruleproposal.KindType {
		t.Errorf("type proposal %+v", p)
	}

	if len(h.gateIn) != 1 || len(h.written) != 1 {
		t.Fatalf("gate calls %d, writes %d", len(h.gateIn), len(h.written))
	}
	w := h.written[0]
	if len(w) != 2 || w[0].Phrase != "ships off" || w[1].Phrase != "lands on the shelf" || w[1].Target != "injure" ||
		w[1].TargetKind != "action" || w[1].ProposalID != ps["lands on the shelf"].ID {
		t.Errorf("written %+v", w)
	}
	for _, want := range []string{"1 generic → injury", "Ezo lands on the shelf with ankle", "phrase shorter than", "stadium renovation"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRun_SecondRunDoesNotResurrectRejected(t *testing.T) {
	h := newHarness(t)
	h.run(t, "a\nr\n")
	h.llm.topicCalls = 0
	h.llm.batchSizes = nil

	res, out := h.run(t, "")
	if res.Paused || res.Approved != 0 || res.Rejected != 0 || res.Applied != 0 {
		t.Fatalf("second run %+v\n%s", res, out)
	}
	if strings.Contains(out, "Action:") {
		t.Errorf("second run prompted:\n%s", out)
	}
	ps := h.byPhrase(t)
	if ps["inks a deal"].Status != eventruleproposal.StatusRejected || ps["inks"].Status != eventruleproposal.StatusRejected {
		t.Errorf("rejected resurrected: %+v", ps)
	}
	if len(h.gateIn) != 1 {
		t.Errorf("gate called again: %d", len(h.gateIn))
	}
}

func TestRun_UnreachableLLM(t *testing.T) {
	h := newHarness(t)
	h.llm.topicErr = errors.New("connection refused")
	h.llm.phraseErr = errors.New("connection refused")
	res, out := h.run(t, "a\n")
	if !res.DiscoveryFailed || res.Proposed != 0 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "connection refused") {
		t.Errorf("output:\n%s", out)
	}
	if len(h.byPhrase(t)) != 0 || len(h.gateIn) != 0 {
		t.Error("proposals stored or gate called after outage")
	}
}

func TestRun_PartialFailureKeepsSuccessfulBatches(t *testing.T) {
	h := newHarness(t)
	h.llm.topicErr = errors.New("timeout")
	res, out := h.run(t, "s\ns\n")
	if res.DiscoveryFailed || res.Proposed != 3 || res.Skipped != 2 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("no partial warning:\n%s", out)
	}
}

func TestRun_QuitKeepsEarlierDecisions(t *testing.T) {
	h := newHarness(t)
	res, out := h.run(t, "a\nq\n")
	if !res.Paused || res.Approved != 1 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	ps := h.byPhrase(t)
	if ps["lands on the shelf"].Status != eventruleproposal.StatusApplied {
		t.Errorf("approval lost: %+v", ps["lands on the shelf"])
	}
	if ps["inks a deal"].Status != eventruleproposal.StatusPending {
		t.Errorf("unreviewed proposal changed: %+v", ps["inks a deal"])
	}
	if ps["inks"].Status != eventruleproposal.StatusRejected {
		t.Errorf("auto-reject lost: %+v", ps["inks"])
	}
	if len(h.written) != 1 || phrasesOf(h.written[0]) != "ships off|lands on the shelf" {
		t.Errorf("written file must exclude unreviewed phrases: %+v", h.written)
	}
}

func TestRun_EOFPauses(t *testing.T) {
	h := newHarness(t)
	res, _ := h.run(t, "a\n")
	if !res.Paused || res.Approved != 1 {
		t.Fatalf("result %+v", res)
	}
}

func TestRun_GateFailureWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped from 0.9000 to 0.8000"}
	res, out := h.run(t, "a\nr\n")
	if !res.GateBlocked || res.GateReason != h.gate.Reason || res.Applied != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(h.written) != 0 {
		t.Fatalf("file written: %+v", h.written)
	}
	if !strings.Contains(out, h.gate.Reason) {
		t.Errorf("reason not printed:\n%s", out)
	}
	p := h.byPhrase(t)["lands on the shelf"]
	if p.Status != eventruleproposal.StatusApproved {
		t.Errorf("status %+v", p)
	}
	if len(h.gateIn) != 1 || phrasesOf(h.gateIn[0]) != "ships off|lands on the shelf" {
		t.Errorf("gate candidate %+v", h.gateIn)
	}
	if !strings.Contains(out, p.ID) || !strings.Contains(out, `"lands on the shelf"`) {
		t.Errorf("blocked output does not list the candidate proposals:\n%s", out)
	}
}

func TestRun_EditPhraseNormalizesAndApproves(t *testing.T) {
	h := newHarness(t)
	// a11 is cited for the original phrase but lacks the edited one, so it
	// must drop out of the edited proposal's evidence.
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) {
		return append(testSample(), eventruleproposal.Sample{Article: article.Article{ID: "a11", Title: "Jones lands on the shelf again"}, ClusterID: "c11"}), nil
	}
	h.llm.phrases[1].ArticleIDs = []string{"a2", "a11"}
	res, out := h.run(t, "e\n  Lands On The Shelf With  \nr\n")
	if res.Approved != 1 || res.Applied != 1 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	ps := h.byPhrase(t)
	if p := ps["lands on the shelf with"]; p.Status != eventruleproposal.StatusApplied || p.Target != "injure" || strings.Join(p.EvidenceIDs, ",") != "a2" {
		t.Errorf("edited %+v", p)
	}
	if p := ps["lands on the shelf"]; p.Status != eventruleproposal.StatusRejected {
		t.Errorf("original %+v", p)
	}
	if w := h.written[0]; w[len(w)-1].Phrase != "lands on the shelf with" {
		t.Errorf("written %+v", w)
	}
}

func TestRun_EditFailingCheckReprompts(t *testing.T) {
	h := newHarness(t)
	res, out := h.run(t, "e\nshelf\ns\nr\n")
	if res.Skipped != 1 || res.Approved != 0 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "phrase shorter than") {
		t.Errorf("no reason for failed edit:\n%s", out)
	}
	if p := h.byPhrase(t)["lands on the shelf"]; p.Status != eventruleproposal.StatusPending {
		t.Errorf("original %+v", p)
	}
}

func statusOf(t *testing.T, h *harness, phrase string) eventruleproposal.Status {
	t.Helper()
	return h.byPhrase(t)[phrase].Status
}

func TestRun_EmptySampleLeavesBacklogUntouched(t *testing.T) {
	h := newHarness(t)
	h.run(t, "s\ns\n")
	calls := len(h.llm.batchSizes)
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) { return nil, nil }

	res, out := h.run(t, "")
	if res.Sampled != 0 || res.Rejected != 0 || res.Paused || res.DiscoveryFailed {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "no generic articles") {
		t.Errorf("no notice:\n%s", out)
	}
	if strings.Contains(out, "Action:") || len(h.llm.batchSizes) != calls || len(h.gateIn) != 0 {
		t.Errorf("empty sample prompted, called the LLM or the gate:\n%s", out)
	}
	if statusOf(t, h, "lands on the shelf") != eventruleproposal.StatusPending || statusOf(t, h, "inks a deal") != eventruleproposal.StatusPending {
		t.Errorf("backlog changed: %+v", h.byPhrase(t))
	}
}

func TestRun_NoMatchesOnlyStaysPendingOtherReasonsReject(t *testing.T) {
	h := newHarness(t)
	h.run(t, "s\ns\n")
	// The next sample no longer contains a2, so "lands on the shelf" has no
	// evidence; "inks a deal" still matches a1 and is prompted.
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) {
		var out []eventruleproposal.Sample
		for _, s := range testSample() {
			if s.Article.ID != "a2" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	res, out := h.run(t, "r\n")
	if res.Rejected != 1 || res.Paused {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "no evidence in this sample") {
		t.Errorf("no notice:\n%s", out)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusPending {
		t.Errorf("no-match proposal %s", got)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusRejected {
		t.Errorf("prompted proposal %s", got)
	}
	if got := statusOf(t, h, "inks"); got != eventruleproposal.StatusRejected {
		t.Errorf("short phrase %s", got)
	}
}

func TestRun_EditOntoQueuedPendingProposalDecidesItOnce(t *testing.T) {
	h := newHarness(t)
	h.llm.phrases = append(h.llm.phrases, appout.EventPhraseProposal{TargetKind: "action", Target: "injure", Phrase: "lands on the shelf with", ArticleIDs: []string{"a2"}})
	res, out := h.run(t, "e\nlands on the shelf with\nr\n")
	if res.Approved != 1 || res.Applied != 1 || res.Rejected != 2 || res.Paused {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if n := strings.Count(out, "Action: [a]"); n != 2 {
		t.Errorf("prompted %d times, want 2:\n%s", n, out)
	}
	if got := statusOf(t, h, "lands on the shelf with"); got != eventruleproposal.StatusApplied {
		t.Errorf("edit target %s", got)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusRejected {
		t.Errorf("original %s", got)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusRejected {
		t.Errorf("inks a deal %s", got)
	}
}

func TestRun_GateBlockedThenOperatorRejectsOffender(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "purity dropped"}
	res, out := h.run(t, "a\na\n")
	if !res.GateBlocked || res.Approved != 2 {
		t.Fatalf("first run %+v\n%s", res, out)
	}
	ps := h.byPhrase(t)
	for _, ph := range []string{"lands on the shelf", "inks a deal"} {
		if !strings.Contains(out, ps[ph].ID) {
			t.Errorf("blocked output lacks %s (%s):\n%s", ph, ps[ph].ID, out)
		}
	}

	h.gate = eventrulecycle.GateResult{OK: true}
	res, out = h.run(t, "r\nk\n")
	if res.GateBlocked || res.Applied != 1 || res.Rejected != 1 {
		t.Fatalf("second run %+v\n%s", res, out)
	}
	if !strings.Contains(out, "approved, not yet applied") {
		t.Errorf("carry-over not labelled:\n%s", out)
	}
	if len(h.gateIn) != 2 || phrasesOf(h.gateIn[1]) != "ships off|inks a deal" {
		t.Errorf("second gate candidate %+v", h.gateIn)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusRejected {
		t.Errorf("offender %s", got)
	}
	if got := statusOf(t, h, "inks a deal"); got != eventruleproposal.StatusApplied {
		t.Errorf("kept %s", got)
	}
	if len(h.written) != 1 || phrasesOf(h.written[0]) != "ships off|inks a deal" {
		t.Errorf("written %+v", h.written)
	}
}

func TestRun_ApprovedRuleAlreadyInFileIsDedupedAndMarkedApplied(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: false, Reason: "blocked"}
	h.run(t, "a\nr\n")
	id := h.byPhrase(t)["lands on the shelf"].ID
	// Simulate a crash after the file was written but before MarkApplied.
	h.existing = append(h.existing, eventrulecycle.AppliedRule{TargetKind: "action", Target: "injure", Phrase: "lands on the shelf", ProposalID: id, PromptVersion: eventrulecycle.EventRulesPromptVersion})
	h.gate = eventrulecycle.GateResult{OK: true}

	res, out := h.run(t, "k\n")
	if res.Applied != 1 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if len(h.written) != 1 || phrasesOf(h.written[0]) != "ships off|lands on the shelf" {
		t.Errorf("written %+v", h.written)
	}
	if got := statusOf(t, h, "lands on the shelf"); got != eventruleproposal.StatusApplied {
		t.Errorf("status %s", got)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("tty gone") }

func TestRun_ScannerErrorIsReturned(t *testing.T) {
	h := newHarness(t)
	res, out, err := h.runWith(bufio.NewScanner(errReader{}))
	if err == nil || !strings.Contains(err.Error(), "tty gone") {
		t.Fatalf("err = %v, res %+v\n%s", err, res, out)
	}
}

func TestRun_NilScannerOnlyFailsWhenPrompting(t *testing.T) {
	h := newHarness(t)
	h.llm.topicErr = errors.New("down")
	h.llm.phraseErr = errors.New("down")
	if _, _, err := h.runWith(nil); err != nil {
		t.Fatalf("nothing to prompt, got %v", err)
	}

	h2 := newHarness(t)
	if _, _, err := h2.runWith(nil); err == nil || !strings.Contains(err.Error(), "scanner") {
		t.Fatalf("prompting without a scanner: err = %v", err)
	}
}

// A cue phrase found only in generic articles of the sample can still
// override the type of an already-typed article; the typed control sample
// lets Check see it and auto-reject the phrase.
func TestRun_CueRetypingATypedControlArticleIsAutoRejected(t *testing.T) {
	h := newHarness(t)
	h.deps.BaseRules = func() (eventsignals.Rules, error) {
		return eventsignals.Rules{ActionTerms: map[eventsignals.Action][]string{eventsignals.ActionTrade: {"trades"}}}, nil
	}
	h.deps.Sampler = func(context.Context) ([]eventruleproposal.Sample, error) {
		return append(testSample(), eventruleproposal.Sample{Article: article.Article{ID: "a11", Title: "The rumor mill churns after a quiet week"}, ClusterID: "c11"}), nil
	}
	h.deps.Control = func(context.Context) ([]eventruleproposal.Sample, error) {
		return []eventruleproposal.Sample{{Article: article.Article{ID: "t1", Title: "Isotopes trades Dale as the rumor mill churns"}, ClusterID: "c90"}}, nil
	}
	h.llm.topics = nil
	h.llm.phrases = []appout.EventPhraseProposal{{TargetKind: "signal", Target: "rumor", Phrase: "rumor mill", ArticleIDs: []string{"a11"}}}

	res, out := h.run(t, "")
	if res.Rejected != 1 || res.Paused || strings.Contains(out, "Action:") {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "retypes 1 already-typed articles") {
		t.Errorf("no retype reason:\n%s", out)
	}
	if got := statusOf(t, h, "rumor mill"); got != eventruleproposal.StatusRejected {
		t.Errorf("status %s", got)
	}
}

func TestRun_PhraseAlreadyInTheVocabularyIsNotProposedNorKept(t *testing.T) {
	h := newHarness(t)
	h.deps.BaseRules = func() (eventsignals.Rules, error) {
		return eventsignals.Rules{ActionTerms: map[eventsignals.Action][]string{
			eventsignals.ActionTrade:  {"Inks A Deal"},
			eventsignals.ActionReport: {"per sources"},
		}}, nil
	}
	seeded, err := h.repo.Upsert(context.Background(), eventruleproposal.Proposal{
		Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetSignal, Target: "rumor", Phrase: "per sources",
		EvidenceIDs: []string{"a1"}, Status: eventruleproposal.StatusPending, PromptVersion: "event-rules-v1", CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, out := h.run(t, "s\n")
	if res.Proposed != 3 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if _, stored := h.byPhrase(t)["inks a deal"]; stored {
		t.Error("a phrase already in another bucket of the vocabulary was proposed")
	}
	p, err := h.repo.Get(context.Background(), seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != eventruleproposal.StatusRejected || !strings.Contains(out, "already in the rule vocabulary (action_terms.report)") {
		t.Errorf("pending duplicate %+v\n%s", p, out)
	}
}

func TestRun_ListsOnlyThisRunsTypeProposals(t *testing.T) {
	h := newHarness(t)
	old, err := h.repo.Upsert(context.Background(), eventruleproposal.Proposal{
		Kind: eventruleproposal.KindType, TypeName: "coaching search", Description: "old one",
		Status: eventruleproposal.StatusPending, PromptVersion: "event-rules-v1", CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, out := h.run(t, "s\ns\n")
	cur := h.byPhrase(t)["type:stadium renovation"]
	if !strings.Contains(out, cur.ID) || !strings.Contains(out, "stadium renovation") {
		t.Errorf("this run's type proposal not listed with its id:\n%s", out)
	}
	if strings.Contains(out, "coaching search") || strings.Contains(out, old.ID) {
		t.Errorf("older pending type proposal listed:\n%s", out)
	}
	if !strings.Contains(out, "1 older pending type proposal(s)") {
		t.Errorf("no count of older pending type proposals:\n%s", out)
	}
}

func TestRun_ReportsHowManyDatasetArticlesTheGateSawChange(t *testing.T) {
	h := newHarness(t)
	h.gate = eventrulecycle.GateResult{OK: true, Affected: 0}
	res, out := h.run(t, "a\nr\n")
	if !res.GateRan || res.GateAffected != 0 || res.Applied != 1 {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if !strings.Contains(out, "gate not exercised (0 dataset articles affected)") {
		t.Errorf("vacuous gate not flagged:\n%s", out)
	}

	h2 := newHarness(t)
	h2.gate = eventrulecycle.GateResult{OK: true, Affected: 3}
	res, out = h2.run(t, "a\nr\n")
	if !res.GateRan || res.GateAffected != 3 || !strings.Contains(out, "3 dataset article(s) affected") || strings.Contains(out, "not exercised") {
		t.Errorf("result %+v\n%s", res, out)
	}
}
