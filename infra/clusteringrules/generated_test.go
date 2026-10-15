// infra/clusteringrules/generated_test.go
package clusteringrules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
)

func baseRules(t *testing.T) clusteringrules.RulesFile {
	t.Helper()
	f, err := clusteringrules.Load("testdata/valid.yml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseGenerated_MissingFileIsEmpty(t *testing.T) {
	g, err := clusteringrules.ParseGenerated(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil || len(g.ActionTerms)+len(g.ContentCues) != 0 {
		t.Fatalf("got %+v, %v", g, err)
	}
}

func TestParseGenerated_RejectsUnknownActionAndBlankPhrase(t *testing.T) {
	for name, body := range map[string]string{
		"unknown action": "meta: {version: 1}\naction_terms:\n  nope:\n    - {phrase: x y, proposal_id: a, prompt_version: v}\n",
		"blank phrase":   "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: \"  \", proposal_id: a, prompt_version: v}\n",
		"unknown field":  "meta: {version: 1}\nbogus: 1\n",
	} {
		p := filepath.Join(t.TempDir(), "g.yml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := clusteringrules.ParseGenerated(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMergeGenerated_AddsPhraseAndHandWrittenWins(t *testing.T) {
	f := baseRules(t)
	existing := f.ActionTerms[eventsignals.ActionSign][0] // a hand-written sign phrase
	g := clusteringrules.GeneratedRules{ActionTerms: map[eventsignals.Action][]clusteringrules.GeneratedEntry{
		eventsignals.ActionSign:  {{Phrase: "inks a deal", ProposalID: "evr_1"}},
		eventsignals.ActionTrade: {{Phrase: existing, ProposalID: "evr_2"}}, // lives under sign by hand
	}}
	merged, warnings := clusteringrules.MergeGenerated(f, g)
	found := false
	for _, p := range merged.ActionTerms[eventsignals.ActionSign] {
		if p == "inks a deal" {
			found = true
		}
	}
	if !found {
		t.Error("generated phrase not merged")
	}
	for _, p := range merged.ActionTerms[eventsignals.ActionTrade] {
		if p == existing {
			t.Error("hand-written bucket must win; duplicate leaked into trade")
		}
	}
	if len(warnings) != 1 {
		t.Errorf("want 1 warning, got %v", warnings)
	}
	if err := merged.Validate(); err != nil {
		t.Errorf("merged set must validate: %v", err)
	}
	// original is not mutated
	for _, p := range f.ActionTerms[eventsignals.ActionSign] {
		if p == "inks a deal" {
			t.Error("MergeGenerated mutated its input")
		}
	}
}

func TestWriteGenerated_RoundTripAndDeterministic(t *testing.T) {
	g := clusteringrules.GeneratedRules{ContentCues: map[eventsignals.ContentSignal][]clusteringrules.GeneratedEntry{
		eventsignals.ContentSignalRumor: {{Phrase: "b phrase", ProposalID: "2", PromptVersion: "v"}, {Phrase: "a phrase", ProposalID: "1", PromptVersion: "v"}},
	}}
	p := filepath.Join(t.TempDir(), "g.yml")
	if err := clusteringrules.WriteGenerated(p, g); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	got, err := clusteringrules.ParseGenerated(p)
	if err != nil || len(got.ContentCues[eventsignals.ContentSignalRumor]) != 2 {
		t.Fatalf("round trip failed: %+v %v", got, err)
	}
	if err := clusteringrules.WriteGenerated(p, got); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Error("output is not deterministic")
	}
}

func TestLoadWithGenerated_EmptyPathIsNoop(t *testing.T) {
	f := baseRules(t)
	out, warnings, err := clusteringrules.LoadWithGenerated(f, "")
	if err != nil || len(warnings) != 0 || len(out.ActionTerms[eventsignals.ActionSign]) != len(f.ActionTerms[eventsignals.ActionSign]) {
		t.Fatalf("expected no-op, got %v %v", warnings, err)
	}
}

func TestGeneratedRules_LenCountsEveryPhrase(t *testing.T) {
	if n := (clusteringrules.GeneratedRules{}).Len(); n != 0 {
		t.Fatalf("empty Len() = %d", n)
	}
	g := clusteringrules.GeneratedRules{
		ActionTerms: map[eventsignals.Action][]clusteringrules.GeneratedEntry{eventsignals.ActionSign: {{Phrase: "a b"}, {Phrase: "c d"}}},
		ContentCues: map[eventsignals.ContentSignal][]clusteringrules.GeneratedEntry{eventsignals.ContentSignalRumor: {{Phrase: "e f"}}},
	}
	if n := g.Len(); n != 3 {
		t.Fatalf("Len() = %d, want 3", n)
	}
}
