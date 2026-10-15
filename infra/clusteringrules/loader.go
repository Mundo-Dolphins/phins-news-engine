// Package clusteringrules decodes a versioned event-clustering rules YAML
// file (Task 3) into domain/eventsignals's and
// domain/clustering's plain data types. The YAML shape (yamlFile
// etc.) is kept separate from the domain types — same pattern
// infra/dictionaryfile and infra/config
// already use — so no domain package ever carries yaml struct tags.
//
// The plan's YAML requirements are broader than what eventsignals.Rules and
// clustering.Config actually hold: the rules file also carries data that
// belongs to a later representative-ranking task (Task 6) — domain
// priorities, content-type publish rules, and three weights/two thresholds
// that Config deliberately does not duplicate (see Config's doc comment).
// This package parses and structurally validates all of it, but only
// converts the fields eventsignals.Rules and clustering.Config actually
// have (ToEventSignalsRules, ToClusteringConfig); the rest is preserved on
// RulesFile.Representative for Task 6 to consume later without needing to
// touch this loader again.
package clusteringrules

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
)

// -- YAML shape (private, yaml-tagged; never shared with domain types) -----

type yamlMeta struct {
	Version     string `yaml:"version"`
	Name        string `yaml:"name"`
	GeneratedAt string `yaml:"generated_at"`
}

type yamlSynonym struct {
	Phrase    string `yaml:"phrase"`
	Canonical string `yaml:"canonical"`
}

type yamlDateWindowPolicy struct {
	// DefaultWindow is a fallback event window (a duration string, e.g.
	// "24h") applied to any of eventsignals' 12 known EventTypes that
	// temporal_windows does not list explicitly. It has no equivalent
	// field on eventsignals.Rules — it only exists to let a rules-file
	// author avoid repeating an entry for every event type; toEventWindows
	// resolves it away before ToEventSignalsRules is ever called.
	DefaultWindow string `yaml:"default_window"`
}

type yamlEventRules struct {
	ActionTerms      map[string][]string  `yaml:"action_terms"`
	ContentCues      map[string][]string  `yaml:"content_cues"`
	DateWindowPolicy yamlDateWindowPolicy `yaml:"date_window_policy"`
}

type yamlThresholds struct {
	JoinThreshold float64 `yaml:"join_threshold"`
	JoinMargin    float64 `yaml:"join_margin"`
	AmbiguousLow  float64 `yaml:"ambiguous_low"`
	AmbiguousHigh float64 `yaml:"ambiguous_high"`

	// SubstantialUpdateThreshold and RepresentativeSwitchMargin are
	// representative-ranking-only (Task 6): clustering.Config has no field
	// for either. Preserved on RulesFile.Representative.
	SubstantialUpdateThreshold float64 `yaml:"substantial_update_threshold"`
	RepresentativeSwitchMargin float64 `yaml:"representative_switch_margin"`
}

type yamlWeights struct {
	EntityMatch     float64 `yaml:"entity_match"`
	ActionMatch     float64 `yaml:"action_match"`
	EventTypeMatch  float64 `yaml:"event_type_match"`
	OpponentMatch   float64 `yaml:"opponent_match"`
	DateWindowMatch float64 `yaml:"date_window_match"`
	SourcePriority  float64 `yaml:"source_priority"`
	Novelty         float64 `yaml:"novelty"`

	// PrimarySource, Completeness, and ClickbaitPenalty are
	// representative-ranking-only (Task 6): clustering.Config has no field
	// for any of the three. Preserved on RulesFile.Representative.
	PrimarySource    float64 `yaml:"primary_source"`
	Completeness     float64 `yaml:"completeness"`
	ClickbaitPenalty float64 `yaml:"clickbait_penalty"`
}

type yamlContentTypeRule struct {
	WindowHours  int  `yaml:"window_hours"`
	PublishLimit int  `yaml:"publish_limit"`
	AllowUpdates bool `yaml:"allow_updates"`
}

type yamlFile struct {
	Meta                  yamlMeta                       `yaml:"meta"`
	Synonyms              []yamlSynonym                  `yaml:"synonyms"`
	EventRules            yamlEventRules                 `yaml:"event_rules"`
	FieldWeights          map[string]float64             `yaml:"field_weights"`
	TopicGroups           map[string][]string            `yaml:"topic_groups"`
	InjuryTypes           map[string][]string            `yaml:"injury_types"`
	UnknownEntityStoplist []string                       `yaml:"unknown_entity_stoplist"`
	TemporalWindows       map[string]string              `yaml:"temporal_windows"`
	Thresholds            yamlThresholds                 `yaml:"thresholds"`
	Weights               yamlWeights                    `yaml:"weights"`
	DomainPriorities      map[string]float64             `yaml:"domain_priorities"`
	ContentTypeRules      map[string]yamlContentTypeRule `yaml:"content_type_rules"`
}

// -- domain-facing exported shape -------------------------------------------

// ContentTypeRule is one content type's editorial publish policy — how wide
// a clustering window it competes within, how many articles of this type
// may be published from one event, and whether a later article may update
// an earlier one. It is infrastructure's own shape (Task 6 has not defined
// a domain type for this yet); a later representative-ranking task consumes
// it via RulesFile.Representative.ContentTypeRules.
type ContentTypeRule struct {
	Window       time.Duration
	PublishLimit int
	AllowUpdates bool
}

// RepresentativeRankingConfig holds every rules-file field the plan
// requires that neither eventsignals.Rules nor clustering.Config has room
// for: the two representative-specific thresholds, the three
// representative-specific weights, domain priorities (0-100, keyed by
// hostname), and per-content-type publish rules. It is preserved verbatim,
// structurally validated but otherwise uninterpreted, for Task 6's
// representative-ranking config to consume later without needing to touch
// this loader again.
type RepresentativeRankingConfig struct {
	SubstantialUpdateThreshold float64
	RepresentativeSwitchMargin float64

	PrimarySourceWeight    float64
	CompletenessWeight     float64
	ClickbaitPenaltyWeight float64

	// DomainPriorities is a 0-100 editorial credibility score keyed by
	// lowercase hostname (e.g. "isotopes.example.com").
	DomainPriorities map[string]float64
	// ContentTypeRules is keyed by content-type name (e.g. "hard_news",
	// "social_posts", "videos", "analysis", "reactions" — the plan's list;
	// this loader does not require exactly those keys, only that whatever
	// keys are present decode to a structurally valid ContentTypeRule, so a
	// later task can add content types without needing a loader change).
	ContentTypeRules map[string]ContentTypeRule
}

// RulesFile is the fully parsed, structurally validated content of a
// clustering rules YAML file (Task 3). Its Synonyms/ActionTerms/
// ContentCues/Entities/EventWindows fields and its threshold/weight fields
// are exactly the data eventsignals.Rules and clustering.Config need —
// ToEventSignalsRules and ToClusteringConfig only assemble them into those
// two domain shapes, with no further transformation. Representative holds
// everything else the plan's YAML requires that belongs to a later task
// (see RepresentativeRankingConfig).
type RulesFile struct {
	Version     string
	Name        string
	GeneratedAt time.Time

	Synonyms              []eventsignals.Synonym
	ActionTerms           map[eventsignals.Action][]string
	ContentCues           map[eventsignals.ContentSignal][]string
	FieldWeights          map[string]float64
	TopicGroups           map[eventsignals.Topic][]string
	InjuryTypes           map[string][]string
	UnknownEntityStoplist []string
	// Entities is empty after Parse/Load — it is populated only by
	// LoadWithEntityCatalog, which merges in the separately-owned entity
	// catalog (see infra/entitycatalog). Kept on
	// RulesFile (rather than a second return value) so ToEventSignalsRules
	// stays a single, uniform conversion regardless of which loader
	// function populated Entities.
	Entities     []eventsignals.EntityRule
	EventWindows map[eventsignals.EventType]time.Duration

	JoinThreshold float64
	JoinMargin    float64
	AmbiguousLow  float64
	AmbiguousHigh float64

	EntityMatchWeight     float64
	ActionMatchWeight     float64
	EventTypeMatchWeight  float64
	OpponentMatchWeight   float64
	DateWindowMatchWeight float64
	SourcePriorityWeight  float64
	NoveltyMatchWeight    float64

	Representative RepresentativeRankingConfig
}

// ToEventSignalsRules converts f into an eventsignals.Rules. It does no
// validation of its own — call Load (or f.Validate) first.
func (f RulesFile) ToEventSignalsRules() eventsignals.Rules {
	return eventsignals.Rules{
		Version:               f.Version,
		Synonyms:              f.Synonyms,
		ActionTerms:           f.ActionTerms,
		ContentCues:           f.ContentCues,
		Entities:              f.Entities,
		FieldWeights:          f.FieldWeights,
		TopicGroups:           f.TopicGroups,
		EventWindows:          f.EventWindows,
		InjuryTypes:           f.InjuryTypes,
		UnknownEntityStoplist: f.UnknownEntityStoplist,
	}
}

// ToClusteringConfig converts f into a clustering.Config. It does no
// validation of its own — call Load (or f.Validate) first.
func (f RulesFile) ToClusteringConfig() clustering.Config {
	return clustering.Config{
		JoinThreshold: f.JoinThreshold,
		JoinMargin:    f.JoinMargin,
		AmbiguousLow:  f.AmbiguousLow,
		AmbiguousHigh: f.AmbiguousHigh,

		EntityMatchWeight:     f.EntityMatchWeight,
		ActionMatchWeight:     f.ActionMatchWeight,
		EventTypeMatchWeight:  f.EventTypeMatchWeight,
		OpponentMatchWeight:   f.OpponentMatchWeight,
		DateWindowMatchWeight: f.DateWindowMatchWeight,
		SourcePriorityWeight:  f.SourcePriorityWeight,
		NoveltyMatchWeight:    f.NoveltyMatchWeight,

		RulesVersion: f.Version,
	}
}

// defaultDomainPriorityWeight, defaultNoveltyWeight, defaultClarityWeight,
// defaultFreshnessWeight, defaultOriginalContentWeight, and
// defaultEditorialWeight stand in for 6 of clustering.RepresentativeConfig's
// 9 weighted-signal fields that this loader's YAML schema does not carry
// yet — see ToRepresentativeConfig's doc comment for the full explanation.
const (
	defaultDomainPriorityWeight  = 0.15
	defaultNoveltyWeight         = 0.10
	defaultClarityWeight         = 0.05
	defaultFreshnessWeight       = 0.10
	defaultOriginalContentWeight = 0.10
	defaultEditorialWeight       = 0.05

	// defaultDomainPriority (0-100) is used for a domain absent from
	// f.Representative.DomainPriorities — an unrecognized domain is
	// "unknown", not "untrustworthy" (see RepresentativeConfig.
	// DefaultDomainPriority's doc comment), so this sits at the midpoint of
	// the 0-100 scale rather than at either extreme.
	defaultDomainPriority = 50.0

	// defaultFreshnessWindow bounds RepresentativeConfig.FreshnessWeight.
	// Chosen at double the hard_news content_type_rules window (24h, see
	// configs/event_clustering_rules.yml) so a representative candidate
	// published toward the end of its own content type's competing window
	// still earns partial freshness credit rather than an all-or-nothing
	// cliff.
	defaultFreshnessWindow = 48 * time.Hour
)

// ToRepresentativeConfig converts f into a clustering.RepresentativeConfig.
// It does no validation of its own — call Load (or f.Validate) first.
//
// Known gap (documented here rather than silently patched over):
// clustering.RepresentativeConfig has 9 weighted-signal fields plus a
// freshness window and a default domain priority.
// f.Representative (this loader's YAML-facing shape) only carries 3 of
// those 9 weights — primary_source, completeness, clickbait_penalty — plus
// the two representative-only switch thresholds and domain_priorities/
// content_type_rules; see RepresentativeRankingConfig's own doc comment.
// Extending the YAML schema and its validation to cover the other 6 weights
// (domain_priority, novelty, clarity, freshness, original_content,
// editorial) plus a freshness window and a default domain priority is real,
// separate work, and none of those 6 signals has a production-tuned value
// to load yet regardless. This method instead fills them with small,
// explicitly-labeled constants, deliberately weighted lower than the 3
// known/tuned weights so they nudge representative selection rather than
// dominate it until a later task extends the loader.
//
// ToRepresentativeConfig is the single, shared place this conversion
// happens: both engine/run.go (the live service) and
// the tools CLI (the "clustering evaluate" offline tool, Task 9)
// call it, so the 6 filler constants and the ClickbaitPenaltyWeight sign
// fix below can never drift between the two binaries.
func (f RulesFile) ToRepresentativeConfig() clustering.RepresentativeConfig {
	return clustering.RepresentativeConfig{
		PrimarySourceWeight:   f.Representative.PrimarySourceWeight,
		CompletenessWeight:    f.Representative.CompletenessWeight,
		DomainPriorityWeight:  defaultDomainPriorityWeight,
		NoveltyWeight:         defaultNoveltyWeight,
		ClarityWeight:         defaultClarityWeight,
		FreshnessWeight:       defaultFreshnessWeight,
		OriginalContentWeight: defaultOriginalContentWeight,
		EditorialWeight:       defaultEditorialWeight,
		VerifiabilityWeight:   0,
		// ClickbaitPenaltyWeight: clickbait_penalty is authored negative in
		// YAML ("negative by convention here (a penalty subtracted from a
		// representative-ranking score)"), but
		// clustering.RepresentativeConfig.ClickbaitPenaltyWeight expects a
		// non-negative weight: representative.go only applies the penalty
		// when `ClickbaitPenaltyWeight > 0`. Feeding it the loader's raw
		// negative value would fail that guard and silently DISABLE the
		// penalty (equivalent to weight 0), not flip it into a bonus.
		// math.Abs reconciles the two packages' opposite sign conventions
		// without changing either one.
		ClickbaitPenaltyWeight:     math.Abs(f.Representative.ClickbaitPenaltyWeight),
		DomainPriorities:           f.Representative.DomainPriorities,
		DefaultDomainPriority:      defaultDomainPriority,
		FreshnessWindow:            defaultFreshnessWindow,
		SubstantialUpdateThreshold: f.Representative.SubstantialUpdateThreshold,
		RepresentativeSwitchMargin: f.Representative.RepresentativeSwitchMargin,
		RulesVersion:               f.Version,
	}
}

// -- known vocabulary (sourced from eventsignals' own constants, never
// duplicated as bare strings, so this loader can never drift from the
// domain package's actual taxonomy) ------------------------------------

var knownActions = map[eventsignals.Action]bool{
	eventsignals.ActionSign: true, eventsignals.ActionRelease: true, eventsignals.ActionTrade: true,
	eventsignals.ActionWaive: true, eventsignals.ActionClaim: true, eventsignals.ActionExtend: true,
	eventsignals.ActionInjure: true, eventsignals.ActionReturn: true, eventsignals.ActionPractice: true,
	eventsignals.ActionActivate: true, eventsignals.ActionDraft: true, eventsignals.ActionPreview: true,
	eventsignals.ActionResult: true, eventsignals.ActionQuote: true, eventsignals.ActionReport: true,
	eventsignals.ActionConfirm: true, eventsignals.ActionCorrect: true,
	// New in the entity-extraction/fingerprint work — see
	// eventsignals.Action's own doc comment for why these 9 exist
	// alongside the original 17.
	eventsignals.ActionInjuredReserve: true, eventsignals.ActionPracticeSquad: true,
	eventsignals.ActionPromote: true, eventsignals.ActionDemote: true, eventsignals.ActionTryout: true,
	eventsignals.ActionContract: true, eventsignals.ActionGameStatus: true,
	eventsignals.ActionDepthChart: true, eventsignals.ActionRoster: true,
}

var knownContentSignals = map[eventsignals.ContentSignal]bool{
	eventsignals.ContentSignalRumor: true, eventsignals.ContentSignalConfirmation: true,
	eventsignals.ContentSignalCorrection: true, eventsignals.ContentSignalAnalysis: true,
	eventsignals.ContentSignalReaction: true, eventsignals.ContentSignalQuote: true,
}

var knownEventTypes = map[eventsignals.EventType]bool{
	eventsignals.EventTypeTransaction: true, eventsignals.EventTypeInjury: true, eventsignals.EventTypeDraft: true,
	eventsignals.EventTypeGame: true, eventsignals.EventTypePractice: true, eventsignals.EventTypeQuote: true,
	eventsignals.EventTypeRumor: true, eventsignals.EventTypeConfirmation: true, eventsignals.EventTypeCorrection: true,
	eventsignals.EventTypeAnalysis: true, eventsignals.EventTypeReaction: true, eventsignals.EventTypeGeneric: true,
}

// allEventTypes lists knownEventTypes' keys in a fixed order, used to walk
// every event type deterministically when resolving temporal_windows.
var allEventTypes = []eventsignals.EventType{
	eventsignals.EventTypeTransaction, eventsignals.EventTypeInjury, eventsignals.EventTypeDraft,
	eventsignals.EventTypeGame, eventsignals.EventTypePractice, eventsignals.EventTypeQuote,
	eventsignals.EventTypeRumor, eventsignals.EventTypeConfirmation, eventsignals.EventTypeCorrection,
	eventsignals.EventTypeAnalysis, eventsignals.EventTypeReaction, eventsignals.EventTypeGeneric,
}

// -- loading -----------------------------------------------------------

// Parse reads path and decodes it into a RulesFile. It only parses (strict
// decode plus the field-level conversions that can themselves fail, e.g. an
// unparseable date or duration) — it never calls Validate, so a future
// caller can choose fail-fast (Load) or its own report-all behavior on top
// of the same decode step, mirroring dictionaryfile.Load's split.
func Parse(path string) (RulesFile, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return RulesFile{}, fmt.Errorf("clusteringrules: read %s: %w", path, err)
	}

	var yf yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&yf); err != nil {
		return RulesFile{}, fmt.Errorf("clusteringrules: parse %s: %w", path, err)
	}

	f, err := toRulesFile(yf)
	if err != nil {
		return RulesFile{}, fmt.Errorf("clusteringrules: %s: %w", path, err)
	}
	return f, nil
}

// Load reads path, parses it (Parse), validates the result (Validate), and
// returns it — the fail-fast path a caller (e.g. main.go on startup) is
// expected to use. Unlike dictionaryfile.Load, Load here bundles
// validation: nothing in the plan calls for a "clustering-rules validate"
// CLI with dictionary.Validate's report-all/severity-graded audience, so
// there is no second consumer that needs the parse and validate steps kept
// apart at this level — Parse still exists standalone for exactly that if
// one shows up later.
func Load(path string) (RulesFile, error) {
	f, err := Parse(path)
	if err != nil {
		return RulesFile{}, err
	}
	if err := f.Validate(); err != nil {
		return RulesFile{}, fmt.Errorf("clusteringrules: %s: %w", path, err)
	}
	return f, nil
}

// LoadWithEntityCatalog loads rulesPath the same as Load, then loads and
// merges manualEntitiesPath/generatedEntitiesPath (via entitycatalog.Load)
// onto the result's Entities field. All of this repo's callers that need
// a fully usable eventsignals.Rules (the live service, the offline
// "clustering evaluate" tool, and pipeline replay) share this helper so
// they can never drift on how the two entity-catalog files and the rules
// file are combined.
func LoadWithEntityCatalog(rulesPath, manualEntitiesPath, generatedEntitiesPath string) (RulesFile, error) {
	f, err := Load(rulesPath)
	if err != nil {
		return RulesFile{}, err
	}
	entities, err := entitycatalog.Load(manualEntitiesPath, generatedEntitiesPath)
	if err != nil {
		return RulesFile{}, fmt.Errorf("clusteringrules: load entity catalog: %w", err)
	}
	f.Entities = entities
	return f, nil
}

// toRulesFile converts a decoded yamlFile into a RulesFile, performing only
// the conversions that can themselves fail (dates, durations) — range/
// ordering/duplicate checks live in Validate.
func toRulesFile(yf yamlFile) (RulesFile, error) {
	generatedAt, err := parseOptionalDate(yf.Meta.GeneratedAt)
	if err != nil {
		return RulesFile{}, fmt.Errorf("meta.generated_at: %w", err)
	}

	synonyms := make([]eventsignals.Synonym, 0, len(yf.Synonyms))
	for _, s := range yf.Synonyms {
		synonyms = append(synonyms, eventsignals.Synonym{Phrase: s.Phrase, Canonical: s.Canonical})
	}

	actionTerms := make(map[eventsignals.Action][]string, len(yf.EventRules.ActionTerms))
	for k, terms := range yf.EventRules.ActionTerms {
		actionTerms[eventsignals.Action(k)] = terms
	}

	contentCues := make(map[eventsignals.ContentSignal][]string, len(yf.EventRules.ContentCues))
	for k, terms := range yf.EventRules.ContentCues {
		contentCues[eventsignals.ContentSignal(k)] = terms
	}

	fieldWeights := make(map[string]float64, len(yf.FieldWeights))
	for k, v := range yf.FieldWeights {
		fieldWeights[k] = v
	}

	topicGroups := make(map[eventsignals.Topic][]string, len(yf.TopicGroups))
	for k, values := range yf.TopicGroups {
		topicGroups[eventsignals.Topic(k)] = values
	}

	injuryTypes := make(map[string][]string, len(yf.InjuryTypes))
	for k, v := range yf.InjuryTypes {
		injuryTypes[k] = v
	}

	unknownEntityStoplist := make([]string, len(yf.UnknownEntityStoplist))
	copy(unknownEntityStoplist, yf.UnknownEntityStoplist)

	eventWindows, err := toEventWindows(yf.TemporalWindows, yf.EventRules.DateWindowPolicy.DefaultWindow)
	if err != nil {
		return RulesFile{}, err
	}

	domainPriorities := make(map[string]float64, len(yf.DomainPriorities))
	for host, p := range yf.DomainPriorities {
		domainPriorities[host] = p
	}

	contentTypeRules := make(map[string]ContentTypeRule, len(yf.ContentTypeRules))
	for name, r := range yf.ContentTypeRules {
		contentTypeRules[name] = ContentTypeRule{
			Window:       time.Duration(r.WindowHours) * time.Hour,
			PublishLimit: r.PublishLimit,
			AllowUpdates: r.AllowUpdates,
		}
	}

	return RulesFile{
		Version:     yf.Meta.Version,
		Name:        yf.Meta.Name,
		GeneratedAt: timeOrZero(generatedAt),

		Synonyms:              synonyms,
		ActionTerms:           actionTerms,
		ContentCues:           contentCues,
		FieldWeights:          fieldWeights,
		TopicGroups:           topicGroups,
		InjuryTypes:           injuryTypes,
		UnknownEntityStoplist: unknownEntityStoplist,
		EventWindows:          eventWindows,

		JoinThreshold: yf.Thresholds.JoinThreshold,
		JoinMargin:    yf.Thresholds.JoinMargin,
		AmbiguousLow:  yf.Thresholds.AmbiguousLow,
		AmbiguousHigh: yf.Thresholds.AmbiguousHigh,

		EntityMatchWeight:     yf.Weights.EntityMatch,
		ActionMatchWeight:     yf.Weights.ActionMatch,
		EventTypeMatchWeight:  yf.Weights.EventTypeMatch,
		OpponentMatchWeight:   yf.Weights.OpponentMatch,
		DateWindowMatchWeight: yf.Weights.DateWindowMatch,
		SourcePriorityWeight:  yf.Weights.SourcePriority,
		NoveltyMatchWeight:    yf.Weights.Novelty,

		Representative: RepresentativeRankingConfig{
			SubstantialUpdateThreshold: yf.Thresholds.SubstantialUpdateThreshold,
			RepresentativeSwitchMargin: yf.Thresholds.RepresentativeSwitchMargin,
			PrimarySourceWeight:        yf.Weights.PrimarySource,
			CompletenessWeight:         yf.Weights.Completeness,
			ClickbaitPenaltyWeight:     yf.Weights.ClickbaitPenalty,
			DomainPriorities:           domainPriorities,
			ContentTypeRules:           contentTypeRules,
		},
	}, nil
}

// toEventWindows resolves temporal_windows (event-type name -> duration
// string) plus an optional default fallback into a
// map[eventsignals.EventType]time.Duration keyed by every one of
// eventsignals' 12 known EventTypes. An event type present in
// temporal_windows uses its own duration; one absent from it uses
// defaultWindow (parsed the same way) if set. Neither unknown event-type
// keys nor unparseable duration strings are silently dropped — both are
// reported here so Validate never has to re-parse strings.
func toEventWindows(raw map[string]string, defaultWindow string) (map[eventsignals.EventType]time.Duration, error) {
	for k := range raw {
		if !knownEventTypes[eventsignals.EventType(k)] {
			return nil, fmt.Errorf("temporal_windows: unrecognized event type %q", k)
		}
	}

	var fallback time.Duration
	haveFallback := false
	if strings.TrimSpace(defaultWindow) != "" {
		d, err := time.ParseDuration(defaultWindow)
		if err != nil {
			return nil, fmt.Errorf("event_rules.date_window_policy.default_window: invalid duration %q: %w", defaultWindow, err)
		}
		fallback = d
		haveFallback = true
	}

	windows := make(map[eventsignals.EventType]time.Duration, len(allEventTypes))
	for _, et := range allEventTypes {
		if s, ok := raw[string(et)]; ok {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("temporal_windows[%s]: invalid duration %q: %w", et, s, err)
			}
			windows[et] = d
			continue
		}
		if !haveFallback {
			return nil, fmt.Errorf("temporal_windows[%s]: no explicit window and no date_window_policy.default_window to fall back to", et)
		}
		windows[et] = fallback
	}
	return windows, nil
}

func parseOptionalDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q, want YYYY-MM-DD: %w", s, err)
	}
	return &t, nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// -- validation ----------------------------------------------------------

// Validate checks f for the structural and semantic constraints the plan's
// checklist requires: required metadata, threshold ranges/ordering,
// weight finiteness/sign, positive event windows, no duplicate synonym or
// action/content-cue terms, recognized vocabulary keys, and sane
// representative-ranking-only data. It never mutates f.
func (f RulesFile) Validate() error {
	if f.Version == "" {
		return fmt.Errorf("meta.version is required")
	}
	if f.Name == "" {
		return fmt.Errorf("meta.name is required")
	}

	if err := validateThresholds(f); err != nil {
		return err
	}
	if err := validateWeights(f); err != nil {
		return err
	}
	if err := validateFieldWeights(f.FieldWeights); err != nil {
		return err
	}
	if err := validateEventWindows(f.EventWindows); err != nil {
		return err
	}
	if err := validateDuplicateSynonyms(f.Synonyms); err != nil {
		return err
	}
	if err := validateNoCrossBucketDuplicates("action_terms", stringKeyed(f.ActionTerms)); err != nil {
		return err
	}
	if err := validateNoCrossBucketDuplicates("content_cues", stringKeyedSignal(f.ContentCues)); err != nil {
		return err
	}
	if err := validateActionKeys(f.ActionTerms); err != nil {
		return err
	}
	if err := validateContentSignalKeys(f.ContentCues); err != nil {
		return err
	}
	if err := validateRepresentative(f.Representative); err != nil {
		return err
	}
	if err := validateInjuryTypes(f.InjuryTypes); err != nil {
		return err
	}
	if err := validateUnknownEntityStoplist(f.UnknownEntityStoplist); err != nil {
		return err
	}
	return nil
}

// similarityThreshold01 requires v to be a genuine, positive similarity
// score: 0 < v <= 1, mirroring config.NearDuplicateConfig.Validate's
// existing (0,1] convention for this codebase's other similarity
// thresholds (config.go's DefaultThreshold/ShortTextThreshold).
func similarityThreshold01(name string, v float64) error {
	if v <= 0 || v > 1 {
		return fmt.Errorf("%s: must be within (0,1], got %v", name, v)
	}
	return nil
}

// margin01 requires v to be a score gap in [0,1] — unlike a similarity
// threshold, 0 is a meaningful value here ("no margin required"), so the
// lower bound is inclusive.
func margin01(name string, v float64) error {
	if v < 0 || v > 1 {
		return fmt.Errorf("%s: must be within [0,1], got %v", name, v)
	}
	return nil
}

func validateThresholds(f RulesFile) error {
	if err := similarityThreshold01("thresholds.join_threshold", f.JoinThreshold); err != nil {
		return err
	}
	if err := margin01("thresholds.join_margin", f.JoinMargin); err != nil {
		return err
	}
	if err := similarityThreshold01("thresholds.ambiguous_low", f.AmbiguousLow); err != nil {
		return err
	}
	if err := similarityThreshold01("thresholds.ambiguous_high", f.AmbiguousHigh); err != nil {
		return err
	}
	// clustering.Config's doc comment: "Config is only meaningful when
	// AmbiguousLow <= AmbiguousHigh <= JoinThreshold; this package does not
	// validate that ordering itself (Task 3's loader is expected to)."
	if f.AmbiguousLow > f.AmbiguousHigh {
		return fmt.Errorf("thresholds: ambiguous_low (%v) must be <= ambiguous_high (%v)", f.AmbiguousLow, f.AmbiguousHigh)
	}
	if f.AmbiguousHigh > f.JoinThreshold {
		return fmt.Errorf("thresholds: ambiguous_high (%v) must be <= join_threshold (%v)", f.AmbiguousHigh, f.JoinThreshold)
	}
	return nil
}

// nonNegativeWeight requires a finite, non-negative weight — the default
// rule for every weight per Config's doc comment ("All ... should be >= 0
// ... There is no fixed upper bound on any weight").
func nonNegativeWeight(name string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%s: must be finite, got %v", name, v)
	}
	if v < 0 {
		return fmt.Errorf("%s: must be >= 0, got %v", name, v)
	}
	return nil
}

// finiteWeight requires only that v be finite; used for clickbait_penalty,
// the plan's one explicit "penalty" weight, which this loader exempts from
// the non-negative check. Nothing downstream consumes this weight yet
// (Task 6), so this loader does not additionally force it non-positive —
// it only guarantees the value is usable arithmetic, leaving the sign
// convention (subtracted-magnitude vs. signed-delta) to whichever task
// defines the representative-ranking scorer.
func finiteWeight(name string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%s: must be finite, got %v", name, v)
	}
	return nil
}

func validateWeights(f RulesFile) error {
	weights := []struct {
		name string
		v    float64
	}{
		{"weights.entity_match", f.EntityMatchWeight},
		{"weights.action_match", f.ActionMatchWeight},
		{"weights.event_type_match", f.EventTypeMatchWeight},
		{"weights.opponent_match", f.OpponentMatchWeight},
		{"weights.date_window_match", f.DateWindowMatchWeight},
		{"weights.source_priority", f.SourcePriorityWeight},
		{"weights.novelty", f.NoveltyMatchWeight},
		{"weights.primary_source", f.Representative.PrimarySourceWeight},
		{"weights.completeness", f.Representative.CompletenessWeight},
	}
	for _, w := range weights {
		if err := nonNegativeWeight(w.name, w.v); err != nil {
			return err
		}
	}
	return finiteWeight("weights.clickbait_penalty", f.Representative.ClickbaitPenaltyWeight)
}

func validateEventWindows(windows map[eventsignals.EventType]time.Duration) error {
	for _, et := range allEventTypes {
		d, ok := windows[et]
		if !ok || d <= 0 {
			return fmt.Errorf("temporal_windows[%s]: must resolve to a positive duration, got %v", et, d)
		}
	}
	return nil
}

// validateDuplicateSynonyms rejects a synonym Phrase (case-insensitive,
// trimmed) that appears more than once — regardless of whether it maps to
// the same or a different Canonical each time. NormalizeSynonyms sorts by
// phrase length, not declaration order, so two entries for the same phrase
// would silently make one of them dead data; treating that as an error
// keeps the rules file honest about what it actually normalizes.
func validateDuplicateSynonyms(synonyms []eventsignals.Synonym) error {
	seen := make(map[string]bool, len(synonyms))
	for _, s := range synonyms {
		norm := strings.ToLower(strings.TrimSpace(s.Phrase))
		if norm == "" {
			return fmt.Errorf("synonyms: phrase must not be empty")
		}
		if seen[norm] {
			return fmt.Errorf("synonyms: duplicate phrase %q", s.Phrase)
		}
		seen[norm] = true
	}
	return nil
}

// validateNoCrossBucketDuplicates rejects a term (case-insensitive,
// trimmed) that appears more than once across buckets — whether twice
// within the same bucket or once each under two different buckets. Both
// detectAction and detectContentSignal (eventsignals) walk their bucket map
// in a fixed priority order and return the first bucket whose term list
// matches; a term repeated under a second bucket can never actually be
// reached through that second bucket, and a term repeated within one
// bucket is simply redundant data — both are treated as the same class of
// authoring mistake here.
func validateNoCrossBucketDuplicates(section string, buckets map[string][]string) error {
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	seen := make(map[string]string, len(keys))
	for _, bucket := range keys {
		for _, term := range buckets[bucket] {
			norm := strings.ToLower(strings.TrimSpace(term))
			if norm == "" {
				return fmt.Errorf("%s[%s]: term must not be empty", section, bucket)
			}
			if prevBucket, ok := seen[norm]; ok {
				return fmt.Errorf("%s: duplicate term %q (already used under %q, also listed under %q)", section, term, prevBucket, bucket)
			}
			seen[norm] = bucket
		}
	}
	return nil
}

// validateInjuryTypes rejects an empty key or an empty/blank term within
// injuryTypes — structural validation only (this data has no consumer yet,
// see Rules.InjuryTypes's doc comment).
func validateInjuryTypes(injuryTypes map[string][]string) error {
	keys := make([]string, 0, len(injuryTypes))
	for k := range injuryTypes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("injury_types: key must not be empty")
		}
		terms := injuryTypes[key]
		if len(terms) == 0 {
			return fmt.Errorf("injury_types[%s]: must have at least one term", key)
		}
		for _, term := range terms {
			if strings.TrimSpace(term) == "" {
				return fmt.Errorf("injury_types[%s]: term must not be empty", key)
			}
		}
	}
	return nil
}

// validateUnknownEntityStoplist rejects a blank/whitespace-only entry in
// stoplist — structural validation only, mirroring validateInjuryTypes's
// empty-entry check style (this data has no other consumer-side
// validation yet).
func validateUnknownEntityStoplist(stoplist []string) error {
	for i, entry := range stoplist {
		if strings.TrimSpace(entry) == "" {
			return fmt.Errorf("unknown_entity_stoplist[%d]: entry must not be empty", i)
		}
	}
	return nil
}

func stringKeyed(m map[eventsignals.Action][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

func stringKeyedSignal(m map[eventsignals.ContentSignal][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

func validateActionKeys(m map[eventsignals.Action][]string) error {
	for k := range m {
		if !knownActions[k] {
			return fmt.Errorf("action_terms: unrecognized action %q", k)
		}
	}
	return nil
}

func validateContentSignalKeys(m map[eventsignals.ContentSignal][]string) error {
	for k := range m {
		if !knownContentSignals[k] {
			return fmt.Errorf("content_cues: unrecognized content signal %q", k)
		}
	}
	return nil
}

// validateFieldWeights requires exactly the three keys Extract's
// detectEntities reads ("title", "description", "body"), each a finite,
// positive weight.
func validateFieldWeights(fw map[string]float64) error {
	for _, key := range []string{"title", "description", "body"} {
		v, ok := fw[key]
		if !ok {
			return fmt.Errorf("field_weights: missing required key %q", key)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return fmt.Errorf("field_weights[%s]: must be a finite, positive weight, got %v", key, v)
		}
	}
	return nil
}

func validateRepresentative(r RepresentativeRankingConfig) error {
	if err := similarityThreshold01("thresholds.substantial_update_threshold", r.SubstantialUpdateThreshold); err != nil {
		return err
	}
	if err := margin01("thresholds.representative_switch_margin", r.RepresentativeSwitchMargin); err != nil {
		return err
	}
	for host, p := range r.DomainPriorities {
		if host == "" {
			return fmt.Errorf("domain_priorities: hostname key must not be empty")
		}
		if p < 0 || p > 100 {
			return fmt.Errorf("domain_priorities[%s]: must be within [0,100], got %v", host, p)
		}
	}
	for name, rule := range r.ContentTypeRules {
		if rule.Window <= 0 {
			return fmt.Errorf("content_type_rules[%s]: window_hours must be positive", name)
		}
		if rule.PublishLimit < 0 {
			return fmt.Errorf("content_type_rules[%s]: publish_limit must not be negative", name)
		}
	}
	return nil
}
