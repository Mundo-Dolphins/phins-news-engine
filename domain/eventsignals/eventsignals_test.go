package eventsignals_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// mustNewID builds a source.ID for fixture data that is always valid by
// construction (a fixed literal), so a NewID failure here can only mean the
// fixture itself is broken. newArticle (below) is called from many places
// that don't carry a *testing.T, so this panics rather than threading one
// through every call site for an error path that can never fire.
func mustNewID(platform source.Type, raw string) source.ID {
	id, err := source.NewID(platform, raw)
	if err != nil {
		panic(err)
	}
	return id
}

// testRules builds a small, realistic-shaped Rules fixture covering just
// enough vocabulary to exercise every required test scenario. It is not
// meant to be exhaustive — a later YAML-backed rules file will carry the
// real vocabulary.
func testRules() eventsignals.Rules {
	return eventsignals.Rules{
		Version: "test-v1",
		Synonyms: []eventsignals.Synonym{
			{Phrase: "agreed to terms with", Canonical: "sign"},
		},
		ActionTerms: map[eventsignals.Action][]string{
			eventsignals.ActionSign:     {"sign", "signs", "signed"},
			eventsignals.ActionRelease:  {"release", "released", "waived off the roster"},
			eventsignals.ActionTrade:    {"trade", "traded"},
			eventsignals.ActionWaive:    {"waive", "waived"},
			eventsignals.ActionClaim:    {"claim", "claimed off waivers"},
			eventsignals.ActionExtend:   {"extend", "extension"},
			eventsignals.ActionInjure:   {"injury", "injured", "hurt"},
			eventsignals.ActionReturn:   {"returns to practice", "returned to practice"},
			eventsignals.ActionPractice: {"practice report", "limited in practice"},
			eventsignals.ActionActivate: {"activated from injured reserve"},
			eventsignals.ActionDraft:    {"draft", "drafted", "select", "selects", "selected"},
			eventsignals.ActionPreview:  {"preview", "how to watch"},
			eventsignals.ActionResult:   {"defeat", "defeats", "defeated"},
			eventsignals.ActionQuote:    {"says", "said"},
			eventsignals.ActionReport:   {"report:"},
			eventsignals.ActionConfirm:  {"confirms", "confirmed the signing"},
			eventsignals.ActionCorrect:  {"correction:", "clarifies an earlier report"},
		},
		ContentCues: map[eventsignals.ContentSignal][]string{
			eventsignals.ContentSignalRumor:        {"report:", "expected to", "in the mix"},
			eventsignals.ContentSignalConfirmation: {"it's official", "officially signed", "has been confirmed"},
			eventsignals.ContentSignalCorrection:   {"correction:", "editor's note"},
			eventsignals.ContentSignalAnalysis:     {"analysis:", "biggest takeaways"},
			eventsignals.ContentSignalReaction:     {"twitter reacts", "fans react", "reacts to"},
			eventsignals.ContentSignalQuote:        {"press conference"},
		},
		Entities: []eventsignals.EntityRule{
			{CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes", "springfield isotopes"}},
			{CanonicalName: "North Haverbrook Hornets", Kind: eventsignals.EntityKindTeam, Terms: []string{"hornets"}},
			{CanonicalName: "Tavion Dale", Kind: eventsignals.EntityKindPlayer, Terms: []string{"tavion dale", "dale"}},
			{CanonicalName: "Jaylen Wickersham", Kind: eventsignals.EntityKindPlayer, Terms: []string{"jaylen wickersham", "wickersham"}},
			{CanonicalName: "Ezo Quarrington", Kind: eventsignals.EntityKindPlayer, Terms: []string{"ezo quarrington", "ezo"}},
			{CanonicalName: "Orvel Harrowgate Jr.", Kind: eventsignals.EntityKindPlayer, Terms: []string{"orvel harrowgate jr", "orvel harrowgate"}},
			{CanonicalName: "NFL Draft", Kind: eventsignals.EntityKindCompetition, Terms: []string{"nfl draft"}},
		},
		EventWindows: map[eventsignals.EventType]time.Duration{
			eventsignals.EventTypeTransaction: 24 * time.Hour,
			eventsignals.EventTypeInjury:      48 * time.Hour,
			eventsignals.EventTypeDraft:       72 * time.Hour,
			eventsignals.EventTypeGame:        12 * time.Hour,
			eventsignals.EventTypeGeneric:     6 * time.Hour,
		},
	}
}

func newArticle(title, summary string, publishedAt time.Time) article.Article {
	return article.Article{
		ID:          "a1",
		Source:      source.Source{ID: mustNewID(source.TypeRSS, "espn"), Name: "ESPN", Type: source.TypeRSS},
		Domain:      "sportsnet.example.com",
		Title:       title,
		Summary:     summary,
		PublishedAt: publishedAt,
		FetchedAt:   publishedAt.Add(5 * time.Minute),
		ContentType: article.ContentTypeArticle,
	}
}

func evidenceContains(evidence []eventsignals.Evidence, substr string) bool {
	for _, e := range evidence {
		if strings.Contains(strings.ToLower(string(e)), strings.ToLower(substr)) {
			return true
		}
	}
	return false
}

func TestExtract(t *testing.T) {
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	rules := testRules()

	tests := []struct {
		name               string
		title              string
		summary            string
		wantType           eventsignals.EventType
		wantAction         eventsignals.Action
		wantPrimaryEntity  string
		wantTeam           string
		wantOpponent       string
		wantCompetition    string
		wantEvidenceSubstr string
	}{
		{
			name:               "transaction: signing via a synonym phrase",
			title:              "Isotopes agreed to terms with free agent linebacker",
			summary:            "Springfield added depth on defense with the deal.",
			wantType:           eventsignals.EventTypeTransaction,
			wantAction:         eventsignals.ActionSign,
			wantPrimaryEntity:  "Springfield Isotopes",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "sign",
		},
		{
			name:               "rumor: hedged report of a possible signing",
			title:              "Report: Isotopes expected to sign veteran cornerback",
			summary:            "Springfield has been linked to the free agent for weeks.",
			wantType:           eventsignals.EventTypeRumor,
			wantAction:         eventsignals.ActionSign,
			wantPrimaryEntity:  "Springfield Isotopes",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "rumor",
		},
		{
			name:               "confirmation: official language confirms a signing",
			title:              "It's official: Isotopes sign veteran cornerback",
			summary:            "Springfield officially signed the cornerback Wednesday.",
			wantType:           eventsignals.EventTypeConfirmation,
			wantAction:         eventsignals.ActionSign,
			wantPrimaryEntity:  "Springfield Isotopes",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "confirmation",
		},
		{
			name:               "injury update: player returns to practice",
			title:              "Isotopes WR Jaylen Wickersham returns to practice after ankle issue",
			summary:            "Wickersham was a full participant Wednesday, trending toward playing Sunday.",
			wantType:           eventsignals.EventTypeInjury,
			wantAction:         eventsignals.ActionReturn,
			wantPrimaryEntity:  "Jaylen Wickersham",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "returns to practice",
		},
		{
			name:               "game preview",
			title:              "Isotopes vs. Hornets: Week 5 preview and how to watch",
			summary:            "Kickoff is set for Sunday afternoon.",
			wantType:           eventsignals.EventTypeGame,
			wantAction:         eventsignals.ActionPreview,
			wantPrimaryEntity:  "Springfield Isotopes",
			wantTeam:           "Springfield Isotopes",
			wantOpponent:       "North Haverbrook Hornets",
			wantEvidenceSubstr: "preview",
		},
		{
			name:               "game result",
			title:              "Isotopes defeat Hornets to improve to 5-2",
			summary:            "Springfield pulled away in the fourth quarter.",
			wantType:           eventsignals.EventTypeGame,
			wantAction:         eventsignals.ActionResult,
			wantPrimaryEntity:  "Springfield Isotopes",
			wantTeam:           "Springfield Isotopes",
			wantOpponent:       "North Haverbrook Hornets",
			wantEvidenceSubstr: "defeat",
		},
		{
			name:               "draft pick",
			title:              "Isotopes select Orvel Harrowgate Jr. in NFL Draft",
			summary:            "Springfield adds a top receiver prospect in the first round.",
			wantType:           eventsignals.EventTypeDraft,
			wantAction:         eventsignals.ActionDraft,
			wantPrimaryEntity:  "Orvel Harrowgate Jr.",
			wantTeam:           "Springfield Isotopes",
			wantCompetition:    "NFL Draft",
			wantEvidenceSubstr: "draft",
		},
		{
			name:               "quote/reaction: player quote after a loss",
			title:              "\"We've got to be better,\" Isotopes QB Ezo Quarrington says after loss",
			summary:            "Quarrington addressed reporters after Sunday's tough loss.",
			wantType:           eventsignals.EventTypeQuote,
			wantAction:         eventsignals.ActionQuote,
			wantPrimaryEntity:  "Ezo Quarrington",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "says",
		},
		{
			name:               "quote/reaction: fan/social reaction post",
			title:              "Isotopes Twitter reacts to Ezo Quarrington's big win",
			summary:            "Fans flooded social media after the comeback victory.",
			wantType:           eventsignals.EventTypeReaction,
			wantAction:         "",
			wantPrimaryEntity:  "Ezo Quarrington",
			wantTeam:           "Springfield Isotopes",
			wantEvidenceSubstr: "reaction",
		},
		{
			name:              "generic Isotopes article",
			title:             "Isotopes announce new uniform designs for 2026 season",
			summary:           "The team unveiled a refreshed look ahead of training camp.",
			wantType:          eventsignals.EventTypeGeneric,
			wantAction:        "",
			wantPrimaryEntity: "Springfield Isotopes",
			wantTeam:          "Springfield Isotopes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newArticle(tt.title, tt.summary, published)
			got := eventsignals.Extract(a, rules, time.Now())

			if got.EventType != tt.wantType {
				t.Errorf("EventType = %q, want %q", got.EventType, tt.wantType)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.PrimaryEntity != tt.wantPrimaryEntity {
				t.Errorf("PrimaryEntity = %q, want %q", got.PrimaryEntity, tt.wantPrimaryEntity)
			}
			if got.PrimaryTeam != tt.wantTeam {
				t.Errorf("PrimaryTeam = %q, want %q", got.PrimaryTeam, tt.wantTeam)
			}
			if got.Opponent != tt.wantOpponent {
				t.Errorf("Opponent = %q, want %q", got.Opponent, tt.wantOpponent)
			}
			if got.Competition != tt.wantCompetition {
				t.Errorf("Competition = %q, want %q", got.Competition, tt.wantCompetition)
			}
			if !got.EventDate.Equal(published) {
				t.Errorf("EventDate = %v, want %v (article.PublishedAt)", got.EventDate, published)
			}
			if tt.wantEvidenceSubstr != "" && !evidenceContains(got.Evidence, tt.wantEvidenceSubstr) {
				t.Errorf("Evidence = %v, want an entry containing %q", got.Evidence, tt.wantEvidenceSubstr)
			}
			if len(got.ApproximateKeys) == 0 {
				t.Errorf("ApproximateKeys is empty, want at least one candidate key")
			}
			if got.ArticleID != a.ID || got.SourceID != a.Source.ID.String() || got.Domain != a.Domain || got.ContentType != a.ContentType {
				t.Errorf("passthrough fields not copied correctly: got %+v", got)
			}
		})
	}
}

func TestExtract_BareConfirmActionWithoutContentCue_IsConfirmation(t *testing.T) {
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	rules := testRules()

	a := newArticle("Isotopes Confirms Latest Roster Move", "", published)
	got := eventsignals.Extract(a, rules, time.Now())

	if got.Action != eventsignals.ActionConfirm {
		t.Fatalf("Action = %q, want %q (the test fixture assumes the bare 'confirms' term matched)", got.Action, eventsignals.ActionConfirm)
	}
	if got.ContentSignal != "" {
		t.Fatalf("ContentSignal = %q, want empty (the test fixture assumes no content cue phrase matched)", got.ContentSignal)
	}
	if got.EventType != eventsignals.EventTypeConfirmation {
		t.Errorf("EventType = %q, want %q", got.EventType, eventsignals.EventTypeConfirmation)
	}
}

// TestExtract_SameEntityDifferentActionsProduceDifferentApproximateKeys
// guards against clustering "same player, different unrelated events" just
// because they share a primary entity: the strongest approximate key must
// depend on more than the entity alone.
func TestExtract_SameEntityDifferentActionsProduceDifferentApproximateKeys(t *testing.T) {
	rules := testRules()
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)

	signing := eventsignals.Extract(newArticle("Isotopes sign Tavion Dale to extension", "", published), rules, time.Now())
	injury := eventsignals.Extract(newArticle("Tavion Dale injured at practice", "", published), rules, time.Now())

	if signing.ApproximateKeys[0] == injury.ApproximateKeys[0] {
		t.Errorf("strongest approximate keys must differ for different events: got %q for both", signing.ApproximateKeys[0])
	}
}

// TestExtract_ApproximateKeysOrderedStrongestToWeakest checks the
// documented ordering contract: each successive key is a strict prefix (in
// terms of which components it depends on) of, or otherwise no more
// specific than, the previous one — concretely, the last key here must be
// exactly the (slugged) primary entity with no other component.
func TestExtract_ApproximateKeysOrderedStrongestToWeakest(t *testing.T) {
	rules := testRules()
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)

	rep := eventsignals.Extract(newArticle("Isotopes sign Tavion Dale to extension", "", published), rules, time.Now())

	if len(rep.ApproximateKeys) < 2 {
		t.Fatalf("expected multiple approximate keys, got %v", rep.ApproximateKeys)
	}
	weakest := rep.ApproximateKeys[len(rep.ApproximateKeys)-1]
	if weakest != "tavion-dale" {
		t.Errorf("weakest approximate key = %q, want %q", weakest, "tavion-dale")
	}
	strongest := rep.ApproximateKeys[0]
	if len(strongest) <= len(weakest) {
		t.Errorf("strongest key %q should carry more information than weakest key %q", strongest, weakest)
	}
}

// TestExtract_EventWindowComesFromRulesForClassifiedEventType checks that
// Extract looks up the configured window for the EventType it *chose*, not
// a fixed default.
func TestExtract_EventWindowComesFromRulesForClassifiedEventType(t *testing.T) {
	rules := testRules()
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)

	rep := eventsignals.Extract(newArticle("Isotopes sign Tavion Dale to extension", "", published), rules, time.Now())

	if rep.EventType != eventsignals.EventTypeTransaction {
		t.Fatalf("expected EventTypeTransaction, got %q", rep.EventType)
	}
	if rep.EventWindow != 24*time.Hour {
		t.Errorf("EventWindow = %v, want %v (rules.EventWindows[transaction])", rep.EventWindow, 24*time.Hour)
	}
}

// TestExtract_EventDateFallsBackToNowWhenPublishedAtIsZero documents the
// defensive fallback path required by the plan. In the real pipeline,
// article.Validate rejects any article with a zero PublishedAt before it
// can reach Extract, so this path is not currently reachable in
// production — this test exists to pin the documented behavior in case
// that invariant is ever relaxed.
func TestExtract_EventDateFallsBackToNowWhenPublishedAtIsZero(t *testing.T) {
	rules := testRules()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	a := newArticle("Isotopes announce roster move", "", time.Time{})
	rep := eventsignals.Extract(a, rules, now)

	if !rep.EventDate.Equal(now) {
		t.Errorf("EventDate = %v, want fallback now %v", rep.EventDate, now)
	}
}

// TestExtract_EntitiesOpponentAndCompetitionFollowTextOrderNotRulesOrder
// guards against a bug where Entities, Opponent, and Competition were
// picked using the order entity rules happen to be declared in
// Rules.Entities, instead of the order the corresponding entities are
// actually mentioned in the article's text. The rules fixture below
// deliberately declares "Capital City Capitals" before "Shelbyville Sharks", and
// "Pro Bowl" before "AFC Championship" — the opposite of how the article
// text mentions the corresponding entities (Sharks and AFC Championship in
// the title; Capitals and Pro Bowl only in the summary). Title mentions
// always outrank summary mentions (see rankOf), so the correct answer
// (Sharks as Opponent, AFC Championship as Competition, and Entities
// ordered title-then-summary) is the opposite of what rules-declaration
// order would produce.
func TestExtract_EntitiesOpponentAndCompetitionFollowTextOrderNotRulesOrder(t *testing.T) {
	rules := eventsignals.Rules{
		Version: "test-text-order-v1",
		Entities: []eventsignals.EntityRule{
			{CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes"}},
			// Declared before Sharks, but only mentioned in the summary.
			{CanonicalName: "Capital City Capitals", Kind: eventsignals.EntityKindTeam, Terms: []string{"capitals"}},
			// Declared before AFC Championship, but only mentioned in the summary.
			{CanonicalName: "Pro Bowl", Kind: eventsignals.EntityKindCompetition, Terms: []string{"pro bowl"}},
			{CanonicalName: "Shelbyville Sharks", Kind: eventsignals.EntityKindTeam, Terms: []string{"sharks"}},
			{CanonicalName: "AFC Championship", Kind: eventsignals.EntityKindCompetition, Terms: []string{"afc championship"}},
		},
	}
	published := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)

	a := newArticle(
		"Isotopes host Sharks in AFC Championship rematch",
		"Capitals and Pro Bowl representatives watched from home.",
		published,
	)
	got := eventsignals.Extract(a, rules, time.Now())

	if got.Opponent != "Shelbyville Sharks" {
		t.Errorf("Opponent = %q, want %q (mentioned in the title, ahead of Capitals which is only in the summary)", got.Opponent, "Shelbyville Sharks")
	}
	if got.Competition != "AFC Championship" {
		t.Errorf("Competition = %q, want %q (mentioned in the title, ahead of Pro Bowl which is only in the summary)", got.Competition, "AFC Championship")
	}

	wantEntities := []string{"Springfield Isotopes", "Shelbyville Sharks", "AFC Championship", "Capital City Capitals", "Pro Bowl"}
	if strings.Join(got.Entities, ",") != strings.Join(wantEntities, ",") {
		t.Errorf("Entities = %v, want %v (title mentions in title order, then summary mentions in summary order)", got.Entities, wantEntities)
	}
}

func TestNormalizeSynonyms(t *testing.T) {
	synonyms := []eventsignals.Synonym{
		{Phrase: "agreed to terms with", Canonical: "sign"},
		{Phrase: "did not report", Canonical: "absent"},
	}

	got := eventsignals.NormalizeSynonyms("isotopes agreed to terms with a free agent", synonyms)
	want := "isotopes sign a free agent"
	if got != want {
		t.Errorf("NormalizeSynonyms() = %q, want %q", got, want)
	}
}

func TestNormalizeSynonyms_DoesNotMatchInsideALongerWord(t *testing.T) {
	synonyms := []eventsignals.Synonym{{Phrase: "sign", Canonical: "sign_token"}}

	got := eventsignals.NormalizeSynonyms("the coach resigned from the assignment", synonyms)
	want := "the coach resigned from the assignment"
	if got != want {
		t.Errorf("NormalizeSynonyms() = %q, want %q (no false match inside resigned/assignment)", got, want)
	}
}

func TestSlug_IsExportedAndStable(t *testing.T) {
	if got := eventsignals.Slug("Pierce Quenby"); got != "pierce-quenby" {
		t.Errorf("Slug(%q) = %q, want %q", "Pierce Quenby", got, "pierce-quenby")
	}
}

func TestEntityRule_SupportsCoachAndExecutiveKinds(t *testing.T) {
	rules := []eventsignals.EntityRule{
		{ID: "mike-thistlewood", CanonicalName: "Mike Thistlewood", Kind: eventsignals.EntityKindCoach, Terms: []string{"mike thistlewood", "thistlewood"}},
		{ID: "chris-gravenor", CanonicalName: "Chris Gravenor", Kind: eventsignals.EntityKindExecutive, Terms: []string{"chris gravenor"}},
	}
	if rules[0].Kind != "coach" {
		t.Errorf("EntityKindCoach = %q, want %q", rules[0].Kind, "coach")
	}
	if rules[1].Kind != "executive" {
		t.Errorf("EntityKindExecutive = %q, want %q", rules[1].Kind, "executive")
	}
}

func TestActionPriority_InjuredReserveOutranksGenericInjure(t *testing.T) {
	rules := testRules()
	rules.ActionTerms[eventsignals.ActionInjuredReserve] = []string{"placed on injured reserve", "placed on ir"}
	rules.ActionTerms[eventsignals.ActionInjure] = []string{"injury", "injured", "hurt"}

	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := newArticle("Isotopes place Pierce Quenby on IR after knee injury", "Quenby is placed on injured reserve.", published)
	rep := eventsignals.Extract(a, rules, published)

	if rep.Action != eventsignals.ActionInjuredReserve {
		t.Errorf("Action = %q, want %q", rep.Action, eventsignals.ActionInjuredReserve)
	}
}

func TestExtract_BodyFallbackDetectsConcreteRosterMoveForGenericHeadline(t *testing.T) {
	rules := testRules()
	rules.FieldWeights = map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3}
	rules.ActionTerms[eventsignals.ActionInjuredReserve] = []string{"placed on injured reserve", "placed on ir", "on injured reserve"}
	rules.ActionTerms[eventsignals.ActionPracticeSquad] = []string{"practice squad", "signed to the practice squad"}
	rules.Entities = append(rules.Entities, eventsignals.EntityRule{
		ID: "kyle-larkspur", CanonicalName: "Kyle Larkspur", Kind: eventsignals.EntityKindPlayer,
		Terms: []string{"kyle larkspur"},
	})

	published := time.Date(2026, 9, 16, 17, 21, 0, 0, time.UTC)
	a := article.Article{
		ID: "a1", Source: source.Source{ID: mustNewID(source.TypeWeb, "springfield-isotopes-official"), Name: "Springfield Isotopes Official", Type: source.TypeWeb}, Domain: "isotopes.example.com",
		Title:       "Isotopes make roster moves",
		Body:        "The Springfield Isotopes have placed safety Kyle Larkspur on injured reserve, signed cornerback Merritt Tolliver to the active roster, and signed long snapper Cal Wrexley to the practice squad.",
		PublishedAt: published, FetchedAt: published, ContentType: article.ContentTypeArticle,
	}

	rep := eventsignals.Extract(a, rules, published)

	if rep.Action != eventsignals.ActionInjuredReserve {
		t.Errorf("Action = %q, want %q", rep.Action, eventsignals.ActionInjuredReserve)
	}
	if rep.EventType != eventsignals.EventTypeInjury {
		t.Errorf("EventType = %q, want %q", rep.EventType, eventsignals.EventTypeInjury)
	}
	if !evidenceContains(rep.Evidence, "in body") {
		t.Errorf("Evidence = %v, want body fallback evidence", rep.Evidence)
	}
}

func TestNewActionConstants_HaveExpectedStringValues(t *testing.T) {
	tests := map[eventsignals.Action]string{
		eventsignals.ActionInjuredReserve: "injured-reserve",
		eventsignals.ActionPracticeSquad:  "practice-squad",
		eventsignals.ActionPromote:        "promotion",
		eventsignals.ActionDemote:         "demotion",
		eventsignals.ActionTryout:         "tryout",
		eventsignals.ActionContract:       "contract",
		eventsignals.ActionGameStatus:     "game-status",
		eventsignals.ActionDepthChart:     "depth-chart",
		eventsignals.ActionRoster:         "roster",
	}
	for action, want := range tests {
		if string(action) != want {
			t.Errorf("%v = %q, want %q", action, string(action), want)
		}
	}
}

func TestExtract_EntityMentions_RecordFieldAndConfidence(t *testing.T) {
	rules := testRules()
	rules.FieldWeights = map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3}
	rules.Entities = append(rules.Entities, eventsignals.EntityRule{
		ID: "jaylen-wickersham", CanonicalName: "Jaylen Wickersham", Kind: eventsignals.EntityKindPlayer,
		Terms: []string{"jaylen wickersham", "wickersham"},
	})

	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := article.Article{
		ID: "a1", Source: source.Source{ID: mustNewID(source.TypeRSS, "espn"), Name: "ESPN", Type: source.TypeRSS}, Domain: "sportsnet.example.com",
		Title:       "Isotopes WR Jaylen Wickersham returns to practice",
		Summary:     "Springfield Isotopes get a boost on offense.",
		Body:        "Springfield Isotopes coach Mike Thistlewood said the team is thrilled to have Wickersham back.",
		PublishedAt: published, FetchedAt: published, ContentType: article.ContentTypeArticle,
	}

	rep := eventsignals.Extract(a, rules, published)

	var wickersham *eventsignals.EntityMention
	for i := range rep.EntityMentions {
		if rep.EntityMentions[i].ID == "jaylen-wickersham" {
			wickersham = &rep.EntityMentions[i]
		}
	}
	if wickersham == nil {
		t.Fatalf("no EntityMention for jaylen-wickersham in %+v", rep.EntityMentions)
	}
	if wickersham.Field != "title" {
		t.Errorf("Wickersham Field = %q, want %q (earliest mention wins)", wickersham.Field, "title")
	}
	if wickersham.Confidence != 1.0 {
		t.Errorf("Wickersham Confidence = %v, want 1.0", wickersham.Confidence)
	}
}

func TestExtract_PrimaryVsSecondaryEntities_TitleSubjectWins(t *testing.T) {
	rules := testRules()
	rules.FieldWeights = map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3}
	rules.Entities = []eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes", "springfield isotopes"}},
		{ID: "jeff-harrowby", CanonicalName: "Jeff Harrowby", Kind: eventsignals.EntityKindCoach, Terms: []string{"jeff harrowby", "harrowby"}},
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"pierce quenby"}},
		{ID: "marlow-brookfield", CanonicalName: "Marlow Brookfield", Kind: eventsignals.EntityKindPlayer, Terms: []string{"marlow brookfield"}},
		{ID: "jaylen-wickersham", CanonicalName: "Jaylen Wickersham", Kind: eventsignals.EntityKindPlayer, Terms: []string{"jaylen wickersham"}},
	}

	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := article.Article{
		ID: "a1", Source: source.Source{ID: mustNewID(source.TypeRSS, "espn"), Name: "ESPN", Type: source.TypeRSS}, Domain: "sportsnet.example.com",
		Title:       "Jeff Harrowby discusses Pierce Quenby, Marlow Brookfield and Jaylen Wickersham after practice",
		Summary:     "The Isotopes defensive coordinator held court with reporters Wednesday.",
		PublishedAt: published, FetchedAt: published, ContentType: article.ContentTypeArticle,
	}
	rep := eventsignals.Extract(a, rules, published)

	if got := rep.PrimaryEntities; len(got) != 1 || got[0] != "jeff-harrowby" {
		t.Errorf("PrimaryEntities = %v, want [jeff-harrowby]", got)
	}
	wantSecondary := []string{"pierce-quenby", "marlow-brookfield", "jaylen-wickersham"}
	if fmt.Sprint(rep.SecondaryEntities) != fmt.Sprint(wantSecondary) {
		t.Errorf("SecondaryEntities = %v, want %v", rep.SecondaryEntities, wantSecondary)
	}
	if got := rep.Teams; len(got) != 1 || got[0] != "springfield-isotopes" {
		t.Errorf("Teams = %v, want [springfield-isotopes]", got)
	}
}

func TestExtract_Topics_DerivedFromTopicGroups(t *testing.T) {
	rules := testRules()
	rules.ActionTerms[eventsignals.ActionInjuredReserve] = []string{"placed on injured reserve", "placed on ir"}
	rules.TopicGroups = map[eventsignals.Topic][]string{
		eventsignals.Topic("injury"):      {"injured-reserve", "injure"},
		eventsignals.Topic("transaction"): {"injured-reserve", "sign", "release"},
	}

	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := newArticle("Isotopes place Pierce Quenby on IR after knee injury", "Quenby was placed on injured reserve Wednesday.", published)
	rep := eventsignals.Extract(a, rules, published)

	if got := rep.Events; len(got) != 1 || got[0] != "injured-reserve" {
		t.Errorf("Events = %v, want [injured-reserve]", got)
	}
	wantTopics := map[string]bool{"injury": true, "transaction": true}
	if len(rep.Topics) != 2 {
		t.Fatalf("Topics = %v, want exactly 2 topics", rep.Topics)
	}
	for _, tp := range rep.Topics {
		if !wantTopics[tp] {
			t.Errorf("unexpected topic %q in %v", tp, rep.Topics)
		}
	}
}

// fingerprintTestRules builds a Rules fixture for the
// ticket-scenario regression tests. It starts from testRules() and adds
// FieldWeights, an ActionInjuredReserve bucket, TopicGroups, and a small
// entity set covering a team (with two aliases: "isotopes" and "springfield",
// plus the full "springfield isotopes") and two players.
//
// ActionRelease's term list is extended beyond testRules()'s base
// ("release", "released", "waived off the roster") with "moves on from"
// and "cut" so that three differently-worded headlines describing the same
// real-world release event ("release", "moves on from", "cut by") all
// normalize to the same Action — that's the substance of the
// same-event-different-wording scenario below; without these extra terms,
// two of the three headlines would detect no Action at all.
func fingerprintTestRules() eventsignals.Rules {
	rules := testRules()
	rules.FieldWeights = map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3}
	rules.ActionTerms[eventsignals.ActionInjuredReserve] = []string{"placed on injured reserve", "placed on ir"}
	rules.ActionTerms[eventsignals.ActionRelease] = append(rules.ActionTerms[eventsignals.ActionRelease], "moves on from", "cut")
	rules.TopicGroups = map[eventsignals.Topic][]string{
		eventsignals.Topic("injury"):      {"injured-reserve", "injure"},
		eventsignals.Topic("transaction"): {"injured-reserve", "sign", "release", "extend"},
	}
	rules.Entities = []eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, IsPrimaryTeam: true, Terms: []string{"isotopes", "springfield isotopes", "springfield"}},
		{ID: "ansgar-dunwiddie", CanonicalName: "Ansgar Dunwiddie", Kind: eventsignals.EntityKindPlayer, Terms: []string{"ansgar dunwiddie"}},
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"pierce quenby"}},
	}
	return rules
}

// TestExtract_SameEventDifferentWording_ProducesCompatibleFingerprints
// covers the core acceptance scenario: three headlines describing
// the same real-world release of Ansgar Dunwiddie, worded three different
// ways, must all resolve to the same PrimaryEntities and the same Events
// so a later clustering task can recognize them as the same event.
func TestExtract_SameEventDifferentWording_ProducesCompatibleFingerprints(t *testing.T) {
	rules := fingerprintTestRules()
	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	titles := []string{
		"Isotopes release LB Ansgar Dunwiddie",
		"Springfield moves on from veteran linebacker Ansgar Dunwiddie",
		"Ansgar Dunwiddie cut by Isotopes",
	}
	var fingerprints []eventsignals.Fingerprint
	for _, title := range titles {
		a := newArticle(title, "The move was announced Tuesday.", published)
		fingerprints = append(fingerprints, eventsignals.Extract(a, rules, published).Fingerprint())
	}
	for i, fp := range fingerprints {
		if len(fp.PrimaryEntities) != 1 || fp.PrimaryEntities[0] != "ansgar-dunwiddie" {
			t.Errorf("titles[%d] PrimaryEntities = %v, want [ansgar-dunwiddie]", i, fp.PrimaryEntities)
		}
		if len(fp.Events) != 1 || fp.Events[0] != "release" {
			t.Errorf("titles[%d] Events = %v, want [release]", i, fp.Events)
		}
	}
}

// TestExtract_SamePlayerDifferentEvent_SharesEntityDiffersOnTopic covers
// the flip side of the scenario above: the same player named in two
// articles about two genuinely different events (an injury vs. a contract
// extension) must share PrimaryEntity but differ on Topics, so clustering
// does not conflate unrelated coverage just because they share a subject.
func TestExtract_SamePlayerDifferentEvent_SharesEntityDiffersOnTopic(t *testing.T) {
	rules := fingerprintTestRules()
	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	injury := eventsignals.Extract(newArticle("Pierce Quenby suffers knee injury", "Quenby left practice early.", published), rules, published)
	extension := eventsignals.Extract(newArticle("Pierce Quenby signs contract extension", "Quenby agreed to a multi-year deal.", published), rules, published)

	if injury.PrimaryEntity != "Pierce Quenby" || extension.PrimaryEntity != "Pierce Quenby" {
		t.Fatalf("expected both to share PrimaryEntity Pierce Quenby, got %q and %q", injury.PrimaryEntity, extension.PrimaryEntity)
	}
	if fmt.Sprint(injury.Topics) == fmt.Sprint(extension.Topics) {
		t.Errorf("expected differing Topics, both got %v", injury.Topics)
	}
}

// TestExtract_AmbiguousSurname_NoFalsePositivePrimaryEntity covers the
// spec's ambiguous-surname risk: a local-news headline about "Quenby's
// diner" that has nothing to do with Pierce Quenby or the Isotopes must not
// be misread as a Pierce Quenby mention. The entity rule's term is the full
// "pierce quenby" phrase (not the bare surname "quenby"), so "Quenby's diner"
// cannot match it under matchTerm's word-boundary substring matching.
func TestExtract_AmbiguousSurname_NoFalsePositivePrimaryEntity(t *testing.T) {
	rules := fingerprintTestRules()
	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	a := newArticle("Quenby's diner opens downtown", "Longtime local favorite reopens after renovation.", published)
	rep := eventsignals.Extract(a, rules, published)

	if len(rep.PrimaryEntities) != 0 {
		t.Errorf("PrimaryEntities = %v, want none (no Isotopes alias present)", rep.PrimaryEntities)
	}
}

// TestExtract_TeamAliases_NormalizeToSameTeamID covers the team-alias
// normalization scenario: "Springfield Isotopes", "Isotopes", and "Springfield" are
// three surface forms of the same team entity and must all resolve to the
// same Teams ID.
func TestExtract_TeamAliases_NormalizeToSameTeamID(t *testing.T) {
	rules := fingerprintTestRules()
	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for _, title := range []string{"Springfield Isotopes announce roster move", "Isotopes announce roster move", "Springfield announces roster move"} {
		rep := eventsignals.Extract(newArticle(title, "", published), rules, published)
		if len(rep.Teams) != 1 || rep.Teams[0] != "springfield-isotopes" {
			t.Errorf("title %q: Teams = %v, want [springfield-isotopes]", title, rep.Teams)
		}
	}
}

// TestExtract_BodyOnlyMention_NeverBecomesPrimary covers the I4 design
// ruling: an entity mentioned only in the body (nowhere in title/summary)
// is fully visible via EntityMentions/SecondaryEntities at the body
// confidence tier, but is never eligible to become PrimaryEntity or land
// in PrimaryEntities, since body-tier matches are excluded from primary
// selection.
func TestExtract_BodyOnlyMention_NeverBecomesPrimary(t *testing.T) {
	rules := testRules()
	rules.FieldWeights = map[string]float64{"title": 1.0, "description": 0.7, "body": 0.3}
	rules.Entities = append(rules.Entities, eventsignals.EntityRule{
		ID: "jaylen-wickersham", CanonicalName: "Jaylen Wickersham", Kind: eventsignals.EntityKindPlayer,
		Terms: []string{"jaylen wickersham", "wickersham"},
	})

	published := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := article.Article{
		ID: "a1", Source: source.Source{ID: mustNewID(source.TypeRSS, "espn"), Name: "ESPN", Type: source.TypeRSS}, Domain: "sportsnet.example.com",
		Title:       "Teams hold practice ahead of Sunday's games",
		Summary:     "Several squads worked through a light session Wednesday.",
		Body:        "Also on the field was Jaylen Wickersham, who returned after his ankle injury.",
		PublishedAt: published, FetchedAt: published, ContentType: article.ContentTypeArticle,
	}

	rep := eventsignals.Extract(a, rules, published)

	var wickersham *eventsignals.EntityMention
	for i := range rep.EntityMentions {
		if rep.EntityMentions[i].ID == "jaylen-wickersham" {
			wickersham = &rep.EntityMentions[i]
		}
	}
	if wickersham == nil {
		t.Fatalf("no EntityMention for jaylen-wickersham in %+v", rep.EntityMentions)
	}
	if wickersham.Field != "body" {
		t.Errorf("Wickersham Field = %q, want %q", wickersham.Field, "body")
	}
	if wickersham.Confidence != 0.3 {
		t.Errorf("Wickersham Confidence = %v, want 0.3", wickersham.Confidence)
	}

	found := false
	for _, id := range rep.SecondaryEntities {
		if id == "jaylen-wickersham" {
			found = true
		}
	}
	if !found {
		t.Errorf("SecondaryEntities = %v, want to contain jaylen-wickersham", rep.SecondaryEntities)
	}
	for _, id := range rep.PrimaryEntities {
		if id == "jaylen-wickersham" {
			t.Errorf("PrimaryEntities = %v, must not contain a body-only mention", rep.PrimaryEntities)
		}
	}
	if len(rep.PrimaryEntities) != 0 {
		t.Errorf("PrimaryEntities = %v, want none (no title/description person mention)", rep.PrimaryEntities)
	}
	if rep.PrimaryEntity != "" {
		t.Errorf("PrimaryEntity = %q, want empty (body-only matches are excluded from primary selection)", rep.PrimaryEntity)
	}
}

func TestFingerprint_ProjectsRepresentationFields(t *testing.T) {
	rep := eventsignals.Representation{
		PrimaryEntities: []string{"pierce-quenby"}, SecondaryEntities: []string{"marlow-brookfield"},
		Teams: []string{"springfield-isotopes"}, Topics: []string{"injury"}, Events: []string{"injured-reserve"},
	}
	fp := rep.Fingerprint()
	if fmt.Sprint(fp.PrimaryEntities) != "[pierce-quenby]" || fmt.Sprint(fp.Teams) != "[springfield-isotopes]" {
		t.Errorf("Fingerprint() = %+v, did not project Representation fields", fp)
	}
}
