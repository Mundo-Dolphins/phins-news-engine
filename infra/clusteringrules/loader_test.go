package clusteringrules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// -- fixture helpers ---------------------------------------------------

// baseFixture returns the contents of testdata/valid.yml, which every
// mutation test starts from and tweaks with exactly one string
// replacement, so each test only shows the field it cares about instead
// of repeating the whole schema.
func baseFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("read testdata/valid.yml: %v", err)
	}
	return string(data)
}

// mutate writes base with old replaced by new (exactly once) to a temp
// file and returns its path. It fails the test if old is not found, so a
// mutation test can never silently pass by mutating nothing.
func mutate(t *testing.T, base, old, new string) string {
	t.Helper()
	if !strings.Contains(base, old) {
		t.Fatalf("mutate: base fixture does not contain %q", old)
	}
	mutated := strings.Replace(base, old, new, 1)
	return writeFixture(t, mutated)
}

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// -- happy path ----------------------------------------------------------

func TestLoad_ValidFile_DecodesMeta(t *testing.T) {
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if f.Version != "1.0.0-test" || f.Name != "test_clustering_rules" {
		t.Errorf("meta = %+v", f)
	}
	if f.GeneratedAt.IsZero() || f.GeneratedAt.Year() != 2026 {
		t.Errorf("GeneratedAt = %v, want 2026-07-24", f.GeneratedAt)
	}
}

func TestLoad_ToEventSignalsRules_ProducesUsableRules(t *testing.T) {
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rules := f.ToEventSignalsRules()
	if rules.Version != f.Version {
		t.Errorf("rules.Version = %q, want %q", rules.Version, f.Version)
	}

	a := article.Article{
		ID:          "a1",
		Title:       "Isotopes agrees to a deal with Jordan Sample",
		PublishedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
	// This fixture's rules carry no Entities (entities no longer come from
	// this file — see LoadWithEntityCatalog), so this only exercises
	// Action/EventType/EventWindow, not entity extraction.
	rep := eventsignals.Extract(a, rules, time.Now())
	if rep.Action != eventsignals.ActionSign {
		t.Errorf("Action = %q, want %q (synonym normalization should have mapped 'agrees to a deal' -> 'sign')", rep.Action, eventsignals.ActionSign)
	}
	if rep.EventType != eventsignals.EventTypeTransaction {
		t.Errorf("EventType = %q, want %q", rep.EventType, eventsignals.EventTypeTransaction)
	}
	if rep.EventWindow != 48*time.Hour {
		t.Errorf("EventWindow = %v, want 48h (transaction)", rep.EventWindow)
	}
}

func TestLoad_ToEventSignalsRules_EventWindowsFallBackToDefault(t *testing.T) {
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rules := f.ToEventSignalsRules()
	// "generic" is deliberately absent from testdata/valid.yml's
	// temporal_windows so this exercises date_window_policy.default_window.
	got, ok := rules.EventWindows[eventsignals.EventTypeGeneric]
	if !ok || got != 24*time.Hour {
		t.Errorf("EventWindows[generic] = %v, ok=%v, want 24h via default_window fallback", got, ok)
	}
	// every one of the 12 known event types must resolve to a positive
	// window, whether from an explicit entry or the default fallback.
	for _, et := range []eventsignals.EventType{
		eventsignals.EventTypeTransaction, eventsignals.EventTypeInjury, eventsignals.EventTypeDraft,
		eventsignals.EventTypeGame, eventsignals.EventTypePractice, eventsignals.EventTypeQuote,
		eventsignals.EventTypeRumor, eventsignals.EventTypeConfirmation, eventsignals.EventTypeCorrection,
		eventsignals.EventTypeAnalysis, eventsignals.EventTypeReaction, eventsignals.EventTypeGeneric,
	} {
		if rules.EventWindows[et] <= 0 {
			t.Errorf("EventWindows[%s] = %v, want > 0", et, rules.EventWindows[et])
		}
	}
}

func TestLoad_ToClusteringConfig_MapsThresholdsAndWeights(t *testing.T) {
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg := f.ToClusteringConfig()
	if cfg.JoinThreshold != 0.72 || cfg.JoinMargin != 0.08 || cfg.AmbiguousLow != 0.45 || cfg.AmbiguousHigh != 0.60 {
		t.Errorf("thresholds = %+v", cfg)
	}
	if cfg.EntityMatchWeight != 0.35 || cfg.ActionMatchWeight != 0.20 || cfg.EventTypeMatchWeight != 0.15 ||
		cfg.OpponentMatchWeight != 0.10 || cfg.DateWindowMatchWeight != 0.05 ||
		cfg.SourcePriorityWeight != 0.10 || cfg.NoveltyMatchWeight != 0.05 {
		t.Errorf("weights = %+v", cfg)
	}
	if cfg.RulesVersion != f.Version {
		t.Errorf("RulesVersion = %q, want %q", cfg.RulesVersion, f.Version)
	}
}

func TestLoad_RepresentativeRankingData_ParsedForALaterTask(t *testing.T) {
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	rep := f.Representative
	if rep.SubstantialUpdateThreshold != 0.55 {
		t.Errorf("SubstantialUpdateThreshold = %v, want 0.55", rep.SubstantialUpdateThreshold)
	}
	if rep.RepresentativeSwitchMargin != 0.10 {
		t.Errorf("RepresentativeSwitchMargin = %v, want 0.10", rep.RepresentativeSwitchMargin)
	}
	if rep.PrimarySourceWeight != 0.30 || rep.CompletenessWeight != 0.20 || rep.ClickbaitPenaltyWeight != -0.15 {
		t.Errorf("representative weights = %+v", rep)
	}
	if rep.DomainPriorities["isotopes.example.com"] != 100 || rep.DomainPriorities["news.example.org"] != 85 {
		t.Errorf("DomainPriorities = %+v", rep.DomainPriorities)
	}
	hardNews, ok := rep.ContentTypeRules["hard_news"]
	if !ok || hardNews.Window != 24*time.Hour || hardNews.PublishLimit != 1 || !hardNews.AllowUpdates {
		t.Errorf("ContentTypeRules[hard_news] = %+v, ok=%v", hardNews, ok)
	}
	reactions, ok := rep.ContentTypeRules["reactions"]
	if !ok || reactions.PublishLimit != 0 || reactions.AllowUpdates {
		t.Errorf("ContentTypeRules[reactions] = %+v, ok=%v", reactions, ok)
	}
}

// -- field_weights / topic_groups -----------------------------------------

func TestLoad_FieldWeightsAndTopicGroups_Parsed(t *testing.T) {
	path := writeFixture(t, `
meta: {version: "1.0.0", name: "test"}
event_rules:
  action_terms: {sign: ["sign"]}
  content_cues: {}
  date_window_policy: {default_window: "24h"}
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {transaction: [sign], injury: [injure]}
thresholds: {join_threshold: 0.7, join_margin: 0.1, ambiguous_low: 0.4, ambiguous_high: 0.5, substantial_update_threshold: 0.5, representative_switch_margin: 0.1}
weights: {entity_match: 0.3, action_match: 0.2, event_type_match: 0.1, opponent_match: 0.1, date_window_match: 0.0, source_priority: 0.1, novelty: 0.1, primary_source: 0.1, completeness: 0.1, clickbait_penalty: 0.0}
`)
	f, err := clusteringrules.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if f.FieldWeights["title"] != 1.0 || f.FieldWeights["body"] != 0.3 {
		t.Errorf("FieldWeights = %+v, missing expected keys", f.FieldWeights)
	}
	if len(f.TopicGroups[eventsignals.Topic("transaction")]) != 1 {
		t.Errorf("TopicGroups[transaction] = %v, want 1 entry", f.TopicGroups[eventsignals.Topic("transaction")])
	}
	rules := f.ToEventSignalsRules()
	if rules.FieldWeights["title"] != 1.0 {
		t.Errorf("ToEventSignalsRules did not carry FieldWeights through")
	}
}

func TestLoad_NoLongerParsesInlineEntities(t *testing.T) {
	path := writeFixture(t, `
meta: {version: "1.0.0", name: "test"}
event_rules:
  action_terms: {sign: ["sign"]}
  content_cues: {}
  date_window_policy: {default_window: "24h"}
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {}
thresholds: {join_threshold: 0.7, join_margin: 0.1, ambiguous_low: 0.4, ambiguous_high: 0.5, substantial_update_threshold: 0.5, representative_switch_margin: 0.1}
weights: {entity_match: 0.3, action_match: 0.2, event_type_match: 0.1, opponent_match: 0.1, date_window_match: 0.0, source_priority: 0.1, novelty: 0.1, primary_source: 0.1, completeness: 0.1, clickbait_penalty: 0.0}
`)
	f, err := clusteringrules.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(f.Entities) != 0 {
		t.Errorf("Entities = %v, want none — entity_rules.entities is no longer a recognized YAML field", f.Entities)
	}
}

func TestLoad_InjuryTypesAndUnknownEntityStoplist_Parsed(t *testing.T) {
	path := writeFixture(t, `
meta: {version: "1.0.0", name: "test"}
event_rules:
  action_terms: {sign: ["sign"]}
  content_cues: {}
  date_window_policy: {default_window: "24h"}
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {}
injury_types: {knee: ["knee", "knee injury"], acl: ["acl", "torn acl"]}
unknown_entity_stoplist: ["Example Bowl", "All Star Game"]
thresholds: {join_threshold: 0.7, join_margin: 0.1, ambiguous_low: 0.4, ambiguous_high: 0.5, substantial_update_threshold: 0.5, representative_switch_margin: 0.1}
weights: {entity_match: 0.3, action_match: 0.2, event_type_match: 0.1, opponent_match: 0.1, date_window_match: 0.0, source_priority: 0.1, novelty: 0.1, primary_source: 0.1, completeness: 0.1, clickbait_penalty: 0.0}
`)
	f, err := clusteringrules.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(f.InjuryTypes["knee"]) != 2 || f.InjuryTypes["knee"][0] != "knee" {
		t.Errorf("InjuryTypes[knee] = %v, want [knee, knee injury]", f.InjuryTypes["knee"])
	}
	if len(f.UnknownEntityStoplist) != 2 || f.UnknownEntityStoplist[0] != "Example Bowl" {
		t.Errorf("UnknownEntityStoplist = %v, want [Example Bowl, All Star Game]", f.UnknownEntityStoplist)
	}

	rules := f.ToEventSignalsRules()
	if len(rules.InjuryTypes["acl"]) != 2 {
		t.Errorf("ToEventSignalsRules did not carry InjuryTypes through: %v", rules.InjuryTypes)
	}
	if len(rules.UnknownEntityStoplist) != 2 {
		t.Errorf("ToEventSignalsRules did not carry UnknownEntityStoplist through: %v", rules.UnknownEntityStoplist)
	}
}

func TestLoad_RejectsInjuryTypeWithEmptyTermList(t *testing.T) {
	path := writeFixture(t, `
meta: {version: "1.0.0", name: "test"}
event_rules:
  action_terms: {sign: ["sign"]}
  content_cues: {}
  date_window_policy: {default_window: "24h"}
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {}
injury_types: {knee: []}
thresholds: {join_threshold: 0.7, join_margin: 0.1, ambiguous_low: 0.4, ambiguous_high: 0.5, substantial_update_threshold: 0.5, representative_switch_margin: 0.1}
weights: {entity_match: 0.3, action_match: 0.2, event_type_match: 0.1, opponent_match: 0.1, date_window_match: 0.0, source_priority: 0.1, novelty: 0.1, primary_source: 0.1, completeness: 0.1, clickbait_penalty: 0.0}
`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for injury_types[knee] with an empty term list")
	}
}

func TestLoad_RejectsUnknownEntityStoplistWithBlankEntry(t *testing.T) {
	path := writeFixture(t, `
meta: {version: "1.0.0", name: "test"}
event_rules:
  action_terms: {sign: ["sign"]}
  content_cues: {}
  date_window_policy: {default_window: "24h"}
field_weights: {title: 1.0, description: 0.7, body: 0.3}
topic_groups: {}
unknown_entity_stoplist: ["Example Bowl", "  "]
thresholds: {join_threshold: 0.7, join_margin: 0.1, ambiguous_low: 0.4, ambiguous_high: 0.5, substantial_update_threshold: 0.5, representative_switch_margin: 0.1}
weights: {entity_match: 0.3, action_match: 0.2, event_type_match: 0.1, opponent_match: 0.1, date_window_match: 0.0, source_priority: 0.1, novelty: 0.1, primary_source: 0.1, completeness: 0.1, clickbait_penalty: 0.0}
`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for unknown_entity_stoplist with a blank entry")
	}
}

func TestLoad_ValidFile_CarriesProductionInjuryTypesAndStoplist(t *testing.T) {
	// testdata/valid.yml is the shared happy-path fixture (baseFixture) —
	// this asserts Task 2 kept it decodable/valid after adding both new
	// top-level keys.
	f, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(f.InjuryTypes) == 0 {
		t.Error("InjuryTypes is empty, want testdata/valid.yml to carry at least one entry")
	}
	if len(f.UnknownEntityStoplist) == 0 {
		t.Error("UnknownEntityStoplist is empty, want testdata/valid.yml to carry at least one entry")
	}
}

// -- parse vs validate split ---------------------------------------------

func TestParse_DoesNotValidate_LoadDoes(t *testing.T) {
	path := mutate(t, baseFixture(t), "join_threshold: 0.72", "join_threshold: 1.5")

	if _, err := clusteringrules.Parse(path); err != nil {
		t.Fatalf("Parse() error = %v, want Parse to only decode (Validate is a separate step)", err)
	}
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for join_threshold out of (0,1]")
	}
}

// -- structural decode failures ------------------------------------------

func TestLoad_MissingFile(t *testing.T) {
	_, err := clusteringrules.Load(filepath.Join("testdata", "does-not-exist.yml"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing file")
	}
}

func TestLoad_RejectsUnknownTopLevelField(t *testing.T) {
	path := writeFixture(t, baseFixture(t)+"\nunknown_top_level_field: true\n")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unknown top-level field")
	}
}

// -- metadata / required fields -------------------------------------------

func TestLoad_RequiresVersion(t *testing.T) {
	path := mutate(t, baseFixture(t), `version: "1.0.0-test"`, `version: ""`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for empty meta.version")
	}
}

func TestLoad_RequiresName(t *testing.T) {
	path := mutate(t, baseFixture(t), "name: test_clustering_rules", "name: \"\"")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for empty meta.name")
	}
}

func TestLoad_InvalidGeneratedAtDate(t *testing.T) {
	path := mutate(t, baseFixture(t), `generated_at: "2026-07-24"`, `generated_at: "not-a-date"`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unparseable generated_at date")
	}
}

// -- threshold range / ordering -------------------------------------------

func TestLoad_RejectsThresholdAboveOne(t *testing.T) {
	path := mutate(t, baseFixture(t), "join_threshold: 0.72", "join_threshold: 1.5")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for join_threshold > 1")
	}
}

func TestLoad_RejectsZeroThreshold(t *testing.T) {
	path := mutate(t, baseFixture(t), "ambiguous_low: 0.45", "ambiguous_low: 0")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a non-positive similarity threshold")
	}
}

func TestLoad_RejectsBadThresholdOrdering(t *testing.T) {
	path := mutate(t, baseFixture(t), "ambiguous_high: 0.60", "ambiguous_high: 0.90")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error when ambiguous_high > join_threshold")
	}
}

func TestLoad_AllowsZeroJoinMargin(t *testing.T) {
	// join_margin (a score gap, not itself a similarity score) is allowed
	// to be exactly zero: "no margin required" is a meaningful, if
	// permissive, operator choice.
	path := mutate(t, baseFixture(t), "join_margin: 0.08", "join_margin: 0")
	if _, err := clusteringrules.Load(path); err != nil {
		t.Fatalf("Load() error = %v, want join_margin: 0 to be accepted", err)
	}
}

// -- weight validation -----------------------------------------------------

func TestLoad_RejectsNegativeWeight(t *testing.T) {
	path := mutate(t, baseFixture(t), "entity_match: 0.35", "entity_match: -0.1")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a negative non-penalty weight")
	}
}

func TestLoad_AllowsNegativeClickbaitPenalty(t *testing.T) {
	// clickbait_penalty is the plan's one explicit "penalty" weight; it is
	// exempted from the non-negative check (testdata/valid.yml already
	// carries -0.15, and TestLoad_RepresentativeRankingData_* asserts it
	// round-trips).
	if _, err := clusteringrules.Load(filepath.Join("testdata", "valid.yml")); err != nil {
		t.Fatalf("Load() error = %v, want negative clickbait_penalty to be accepted", err)
	}
}

func TestLoad_RejectsNonFiniteWeight(t *testing.T) {
	path := mutate(t, baseFixture(t), "novelty: 0.05", "novelty: .inf")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a non-finite weight")
	}
}

// -- event window validation ------------------------------------------------

func TestLoad_RejectsUnknownEventTypeInTemporalWindows(t *testing.T) {
	path := mutate(t, baseFixture(t), "  reaction: \"12h\"", "  reaction: \"12h\"\n  not_a_real_event_type: \"1h\"")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unrecognized event type key in temporal_windows")
	}
}

func TestLoad_RejectsMissingWindowWithoutDefault(t *testing.T) {
	base := baseFixture(t)
	base = strings.Replace(base, "  default_window: \"24h\"", "  default_window: \"\"", 1)
	path := writeFixture(t, base)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error: 'generic' has no explicit window and no default_window to fall back to")
	}
}

func TestLoad_RejectsUnparseableDuration(t *testing.T) {
	path := mutate(t, baseFixture(t), `transaction: "48h"`, `transaction: "not-a-duration"`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unparseable duration string")
	}
}

// -- duplicate synonym / action / content-cue terms --------------------------

func TestLoad_RejectsDuplicateSynonymPhrase(t *testing.T) {
	_, err := clusteringrules.Load(filepath.Join("testdata", "invalid.yml"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for a case-insensitive duplicate synonym phrase")
	}
}

func TestLoad_RejectsDuplicateActionTermAcrossActions(t *testing.T) {
	path := mutate(t, baseFixture(t), `release: ["release", "released", "cut"]`, `release: ["release", "released", "cut", "sign"]`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error: 'sign' is already an action term under the sign action")
	}
}

func TestLoad_RejectsDuplicateActionTermWithinSameAction(t *testing.T) {
	path := mutate(t, baseFixture(t), `sign: ["sign", "signs", "signed", "agreed to terms"]`, `sign: ["sign", "signs", "signed", "agreed to terms", "Sign"]`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a case-insensitive duplicate term within the same action")
	}
}

func TestLoad_RejectsDuplicateContentCueTermAcrossSignals(t *testing.T) {
	path := mutate(t, baseFixture(t), `analysis: ["analysis", "breakdown"]`, `analysis: ["analysis", "breakdown", "confirmed"]`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error: 'confirmed' is already a content cue under the confirmation signal")
	}
}

// -- unknown vocabulary keys -------------------------------------------------

func TestLoad_RejectsUnknownActionKey(t *testing.T) {
	path := mutate(t, baseFixture(t), `sign: ["sign", "signs", "signed", "agreed to terms"]`, `sign: ["sign", "signs", "signed", "agreed to terms"]
    not_a_real_action: ["whatever"]`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unrecognized action_terms key")
	}
}

func TestLoad_RejectsUnknownContentSignalKey(t *testing.T) {
	path := mutate(t, baseFixture(t), `quote: ["said", "press conference"]`, `quote: ["said", "press conference"]
    not_a_real_signal: ["whatever"]`)
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for an unrecognized content_cues key")
	}
}

// -- representative-ranking-only data structural validation -----------------

func TestLoad_RejectsDomainPriorityOutOfRange(t *testing.T) {
	path := mutate(t, baseFixture(t), "news.example.org: 85", "news.example.org: 150")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a domain priority outside [0,100]")
	}
}

func TestLoad_RejectsContentTypeRuleWithNonPositiveWindow(t *testing.T) {
	path := mutate(t, baseFixture(t), "  hard_news:\n    window_hours: 24", "  hard_news:\n    window_hours: 0")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for content_type_rules.hard_news.window_hours <= 0")
	}
}

func TestLoad_RejectsContentTypeRuleWithNegativePublishLimit(t *testing.T) {
	path := mutate(t, baseFixture(t), "    publish_limit: 0\n    allow_updates: false\n  videos:", "    publish_limit: -1\n    allow_updates: false\n  videos:")
	if _, err := clusteringrules.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a negative publish_limit")
	}
}

// -- the shipped example rules files ------------------------------------

// TestLoad_ExampleRulesFile_LoadsAndProducesUsableRules loads the shipped
// configs/example/event_clustering_rules.yml (not a test fixture), so a
// mistake in the example file itself fails this suite.
func TestLoad_ExampleRulesFile_LoadsAndProducesUsableRules(t *testing.T) {
	f, err := clusteringrules.Load(repofs.ExampleConfig(t, "event_clustering_rules.yml"))
	if err != nil {
		t.Fatalf("Load(configs/example/event_clustering_rules.yml) error = %v", err)
	}
	if f.Version == "" || f.Name == "" {
		t.Errorf("meta = %+v", f)
	}

	// entities no longer come from this file — see LoadWithEntityCatalog —
	// so this only exercises Action/EventType, not entity extraction.
	rules := f.ToEventSignalsRules()
	a := article.Article{
		ID:          "prod-1",
		Title:       "Isotopes agrees to a deal with Jordan Sample on a contract extension",
		PublishedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
	rep := eventsignals.Extract(a, rules, time.Now())
	if rep.EventType != eventsignals.EventTypeTransaction {
		t.Errorf("EventType = %q, want %q", rep.EventType, eventsignals.EventTypeTransaction)
	}

	cfg := f.ToClusteringConfig()
	if cfg.AmbiguousLow > cfg.AmbiguousHigh || cfg.AmbiguousHigh > cfg.JoinThreshold {
		t.Errorf("threshold ordering violated: %+v", cfg)
	}

	if len(f.Representative.DomainPriorities) == 0 {
		t.Error("Representative.DomainPriorities is empty, want the example file's domain priority list")
	}
	if len(f.Representative.ContentTypeRules) == 0 {
		t.Error("Representative.ContentTypeRules is empty, want the example file's content-type rules")
	}
}

// TestLoadWithEntityCatalog_ExampleFiles_LoadsAndProducesUsableRules loads
// the shipped three-file example catalog (rules + manual entities +
// generated entities) from configs/example, so a mistake in any of those
// files fails this suite. It is also the regression test for the C2
// surname-alias fix: "Fixture" alone, with no full name anywhere in the
// text, must still resolve to casey-fixture.
func TestLoadWithEntityCatalog_ExampleFiles_LoadsAndProducesUsableRules(t *testing.T) {
	f, err := clusteringrules.LoadWithEntityCatalog(
		repofs.ExampleConfig(t, "event_clustering_rules.yml"),
		repofs.ExampleConfig(t, "entity_catalog_manual.yml"),
		repofs.ExampleConfig(t, "entity_catalog_generated.yml"),
	)
	if err != nil {
		t.Fatalf("LoadWithEntityCatalog(production files) error = %v", err)
	}

	if len(f.Entities) < 10 {
		t.Errorf("len(f.Entities) = %d, want >= 10 (the example catalog ships 12)", len(f.Entities))
	}

	primaryTeamCount := 0
	var primaryTeamID string
	seenIDs := make(map[string]bool, len(f.Entities))
	for _, e := range f.Entities {
		if e.IsPrimaryTeam {
			primaryTeamCount++
			primaryTeamID = e.ID
		}
		if seenIDs[e.ID] {
			t.Errorf("duplicate entity id %q across the merged catalog", e.ID)
		}
		seenIDs[e.ID] = true
	}
	if primaryTeamCount != 1 {
		t.Errorf("primaryTeamCount = %d, want exactly 1", primaryTeamCount)
	}
	if primaryTeamID != "springfield-isotopes" {
		t.Errorf("primary team ID = %q, want %q", primaryTeamID, "springfield-isotopes")
	}

	rules := f.ToEventSignalsRules()
	a := article.Article{
		ID:          "prod-2",
		Title:       "Fixture returns to practice after ankle injury",
		PublishedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
	rep := eventsignals.Extract(a, rules, time.Now())
	found := false
	for _, id := range rep.PrimaryEntities {
		if id == "casey-fixture" {
			found = true
		}
	}
	if !found {
		t.Errorf("PrimaryEntities = %v, want to contain %q (surname-only alias regression)", rep.PrimaryEntities, "casey-fixture")
	}
}
