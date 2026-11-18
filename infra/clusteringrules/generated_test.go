// infra/clusteringrules/generated_test.go
package clusteringrules_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestParseGenerated_RejectsBlankPhraseAndUnknownField(t *testing.T) {
	for name, body := range map[string]string{
		"blank phrase":  "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: \"  \", proposal_id: a, prompt_version: v}\n",
		"unknown field": "meta: {version: 1}\nbogus: 1\n",
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

// ParseGenerated has no taxonomy to check bucket names against: that check
// lives in LoadWithGenerated, against the hand-written rules' taxonomy.
func TestParseGenerated_AcceptsAnyBucketName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.yml")
	body := "meta: {version: 1}\naction_terms:\n  nope:\n    - {phrase: x y, proposal_id: a, prompt_version: v}\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := clusteringrules.ParseGenerated(p)
	if err != nil || len(g.ActionTerms["nope"]) != 1 {
		t.Fatalf("got %+v, %v", g, err)
	}
}

func TestLoadWithGenerated_RejectsUndeclaredActionAndSignal(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"unknown action": {"meta: {version: 1}\naction_terms:\n  nope:\n    - {phrase: x y, proposal_id: a, prompt_version: v}\n", `unknown action "nope"`},
		"unknown signal": {"meta: {version: 1}\ncontent_cues:\n  hedge:\n    - {phrase: x y, proposal_id: a, prompt_version: v}\n", `unknown content signal "hedge"`},
	} {
		p := filepath.Join(t.TempDir(), "g.yml")
		if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := clusteringrules.LoadWithGenerated(baseRules(t), p)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", name, err, tc.want)
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

// writeGenerated writes body to a fresh generated rules file and returns its path.
func writeGenerated(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "g.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const generatedWithType = `meta: {version: 1}
event_types:
  - {name: kicking-error, description: "A missed kick.", flags: [referential], window: 72h, proposal_id: evt_1, prompt_version: v4}
action_terms:
  kicking-error:
    - {phrase: missed field goal, proposal_id: evt_1, prompt_version: v4}
    - {phrase: shanked punt, proposal_id: evt_1, prompt_version: v4}
`

func TestGeneratedTypes_RoundTrip(t *testing.T) {
	p := writeGenerated(t, generatedWithType)
	g, err := clusteringrules.ParseGenerated(p)
	if err != nil {
		t.Fatal(err)
	}
	want := clusteringrules.GeneratedType{
		Name: "kicking-error", Description: "A missed kick.", Flags: []string{"referential"},
		Window: "72h", ProposalID: "evt_1", PromptVersion: "v4",
	}
	if len(g.Types) != 1 || !reflect.DeepEqual(g.Types[0], want) {
		t.Fatalf("types = %+v, want [%+v]", g.Types, want)
	}
	if n := g.Len(); n != 2 {
		t.Errorf("Len() = %d, want 2 (phrases only)", n)
	}
	out := filepath.Join(t.TempDir(), "out.yml")
	if err := clusteringrules.WriteGenerated(out, g); err != nil {
		t.Fatal(err)
	}
	again, err := clusteringrules.ParseGenerated(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, g) {
		t.Errorf("re-parsed %+v, want %+v", again, g)
	}
}

func TestWriteGenerated_SortsTypesByName(t *testing.T) {
	entry := []clusteringrules.GeneratedEntry{{Phrase: "x y", ProposalID: "p"}}
	g := clusteringrules.GeneratedRules{
		Types: []clusteringrules.GeneratedType{
			{Name: "zeta-event", Window: "48h", ProposalID: "p"},
			{Name: "alpha-event", Window: "48h", ProposalID: "p"},
		},
		ActionTerms: map[eventsignals.Action][]clusteringrules.GeneratedEntry{"zeta-event": entry, "alpha-event": {{Phrase: "z w", ProposalID: "p"}}},
	}
	p := filepath.Join(t.TempDir(), "g.yml")
	if err := clusteringrules.WriteGenerated(p, g); err != nil {
		t.Fatal(err)
	}
	got, err := clusteringrules.ParseGenerated(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Types) != 2 || got.Types[0].Name != "alpha-event" || got.Types[1].Name != "zeta-event" {
		t.Errorf("types = %+v, want sorted by name", got.Types)
	}
}

func TestLoadWithGenerated_MergesType(t *testing.T) {
	f := baseRules(t)
	merged, warnings, err := clusteringrules.LoadWithGenerated(f, writeGenerated(t, generatedWithType))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	tax := merged.Taxonomy
	if got := tax.Classify("kicking-error", ""); got != "kicking-error" {
		t.Errorf("Classify = %q, want kicking-error", got)
	}
	prio := tax.ActionPriority()
	if prio[len(prio)-1] != "kicking-error" || len(prio) != len(f.Taxonomy.ActionPriority())+1 {
		t.Errorf("ActionPriority = %v, want hand-written actions then kicking-error", prio)
	}
	for _, a := range tax.BodyFallbackPriority() {
		if a == "kicking-error" {
			t.Error("a generated action must never be body fallback")
		}
	}
	if got := merged.EventWindows["kicking-error"]; got != 72*time.Hour {
		t.Errorf("window = %v, want 72h", got)
	}
	if !tax.HasFlag("kicking-error", eventsignals.FlagReferential) {
		t.Error("referential flag not merged")
	}
	if len(merged.ActionTerms["kicking-error"]) != 2 {
		t.Errorf("phrases = %v, want 2", merged.ActionTerms["kicking-error"])
	}
	// the input is not mutated
	if len(f.Taxonomy.EventTypes) != len(baseRules(t).Taxonomy.EventTypes) || len(f.Taxonomy.Actions) != len(baseRules(t).Taxonomy.Actions) {
		t.Error("LoadWithGenerated mutated its input taxonomy")
	}
	if _, ok := f.EventWindows["kicking-error"]; ok {
		t.Error("LoadWithGenerated mutated its input windows")
	}
}

func TestLoadWithGenerated_LegacyFileWithoutTypes(t *testing.T) {
	f := baseRules(t)
	body := "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: inks a deal, proposal_id: a, prompt_version: v}\n"
	merged, warnings, err := clusteringrules.LoadWithGenerated(f, writeGenerated(t, body))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("got %v, %v", warnings, err)
	}
	if !reflect.DeepEqual(merged.Taxonomy, f.Taxonomy) || !reflect.DeepEqual(merged.EventWindows, f.EventWindows) {
		t.Error("a file with no event_types must leave the taxonomy and windows unchanged")
	}
}

func TestParseGenerated_RejectsBadType(t *testing.T) {
	typ := func(fields string) string {
		return "meta: {version: 1}\nevent_types:\n  - {" + fields + "}\n"
	}
	for name, tc := range map[string]struct{ body, want string }{
		"missing window":     {typ("name: kicking-error, proposal_id: p"), "window"},
		"unparseable window": {typ("name: kicking-error, window: soon, proposal_id: p"), "window"},
		"zero window":        {typ("name: kicking-error, window: 0s, proposal_id: p"), "window"},
		"negative window":    {typ("name: kicking-error, window: -1h, proposal_id: p"), "window"},
		"unknown flag":       {typ("name: kicking-error, window: 48h, proposal_id: p, flags: [bogus]"), `flag "bogus"`},
		"update_framing":     {typ("name: kicking-error, window: 48h, proposal_id: p, flags: [update_framing]"), `flag "update_framing"`},
		"no proposal_id":     {typ("name: kicking-error, window: 48h"), "proposal_id"},
		"bad name":           {typ("name: Kicking Error, window: 48h, proposal_id: p"), "must match"},
		"duplicate name": {
			"meta: {version: 1}\nevent_types:\n  - {name: kicking-error, window: 48h, proposal_id: p}\n  - {name: kicking-error, window: 24h, proposal_id: q}\n",
			`duplicate name "kicking-error"`,
		},
	} {
		_, err := clusteringrules.ParseGenerated(writeGenerated(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestLoadWithGenerated_RejectsTypeDeclaredByHand(t *testing.T) {
	for name, slug := range map[string]string{"hand-written type": "injury", "hand-written action": "sign"} {
		body := "meta: {version: 1}\nevent_types:\n  - {name: " + slug + ", window: 48h, proposal_id: p}\naction_terms:\n  " + slug + ":\n    - {phrase: brand new phrase, proposal_id: p, prompt_version: v}\n"
		p := writeGenerated(t, body)
		_, _, err := clusteringrules.LoadWithGenerated(baseRules(t), p)
		want := "clusteringrules: " + p + `: generated event type "` + slug + `" is already declared in the hand-written taxonomy`
		if err == nil || err.Error() != want {
			t.Errorf("%s: error = %v, want %q", name, err, want)
		}
	}
}

func TestWriteGenerated_RefusesTypeWithoutPhrase(t *testing.T) {
	g := clusteringrules.GeneratedRules{Types: []clusteringrules.GeneratedType{{Name: "kicking-error", Window: "48h", ProposalID: "p"}}}
	err := clusteringrules.WriteGenerated(filepath.Join(t.TempDir(), "g.yml"), g)
	want := `clusteringrules: generated event type "kicking-error" has no phrase`
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestLoadWithGenerated_InertTypeWarns(t *testing.T) {
	f := baseRules(t)
	existing := f.ActionTerms[eventsignals.ActionSign][0]
	body := "meta: {version: 1}\nevent_types:\n  - {name: kicking-error, window: 48h, proposal_id: p}\naction_terms:\n  kicking-error:\n    - {phrase: \"" + existing + "\", proposal_id: p, prompt_version: v}\n"
	merged, warnings, err := clusteringrules.LoadWithGenerated(f, writeGenerated(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := `generated event type "kicking-error" has no phrase; it is inert`
	if len(warnings) != 2 || warnings[1] != want {
		t.Fatalf("warnings = %q, want the skipped phrase then %q", warnings, want)
	}
	if got := merged.Taxonomy.Classify("kicking-error", ""); got != "kicking-error" {
		t.Errorf("inert type must be kept, Classify = %q", got)
	}
}
