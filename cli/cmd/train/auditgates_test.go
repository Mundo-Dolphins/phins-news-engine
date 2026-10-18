package train

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
)

func testGateAudit(t *testing.T) gateAudit {
	t.Helper()
	m, err := dictionary.Prepare(dictionary.Dictionary{
		Version: "t",
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Confidence: 1}}},
			{Name: "venue", Weight: 12, StrongOverride: true, Entries: []dictionary.Entry{{CanonicalName: "Isotopes Park", Confidence: 1}}},
			{Name: "roster", Weight: 8, Entries: []dictionary.Entry{{CanonicalName: "jones", Confidence: 0.5}, {CanonicalName: "Bart Simpson", Confidence: 0.9}}},
		},
		Decision: dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateAudit{
		matcher: m, text: normalization.TextConfig{ShingleSize: 3, MinTokenLength: 3},
		cls: classification.AnchorPolicy{
			Enabled: true, MinConfidence: 0.7,
			Categories:          map[string]bool{"official_names": true, "roster": true},
			MultiWordCategories: map[string]bool{"roster": true},
		},
		trusted: map[string]bool{"web:official": true}, context: map[string]bool{}, exempt: map[string]bool{},
		veto:      &editorialveto.Config{Rules: []editorialveto.Rule{{Category: "gambling", ReasonCode: "veto:gambling", Patterns: []string{"prop bets"}}}},
		evaluated: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
	}
}

func TestAuditGatesFlagsOnlyPublishRowsTheGatesWouldBlock(t *testing.T) {
	g := testGateAudit(t)
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "ok", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes win again"},
		{ArticleID: "venue-only", Label: "publish", SourceID: "rss:x", Title: "Weather at Isotopes Park tonight"},
		{ArticleID: "surname", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes lose", Body: ""},
		{ArticleID: "veto", Label: "publish", SourceID: "rss:x", Title: "Springfield Isotopes prop bets"},
		{ArticleID: "weak-name", Label: "publish", SourceID: "rss:x", Title: "Jones was great and Bart Simpson too"},
		{ArticleID: "trusted", Label: "publish", SourceID: "web:official", Title: "Weather at Isotopes Park tonight"},
		{ArticleID: "already-reject", Label: "reject", SourceID: "rss:x", Title: "Weather at Isotopes Park tonight"},
	}
	got := map[string]string{}
	for _, c := range g.auditGates(examples) {
		got[c.ArticleID] = c.Reason
	}
	for _, id := range []string{"ok", "trusted", "already-reject", "surname", "weak-name"} {
		if _, bad := got[id]; bad {
			t.Errorf("%s must not be flagged, got %q", id, got[id])
		}
	}
	if r := got["veto"]; r == "" || r[:5] != "veto:" {
		t.Errorf("veto row reason = %q, want veto: prefix", r)
	}
	if got["venue-only"] == "" {
		t.Error("venue-only row must be flagged as having no anchor")
	}
}
