package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeSourcePolicyAuditRepo struct {
	records   []out.SourcePolicyDecisionRecord
	byArticle []out.SourcePolicyDecisionRecord
}

func (f *fakeSourcePolicyAuditRepo) Save(context.Context, out.SourcePolicyDecisionRecord) error {
	return nil
}

func (f *fakeSourcePolicyAuditRepo) ByArticle(context.Context, string) ([]out.SourcePolicyDecisionRecord, error) {
	return f.byArticle, nil
}

func (f *fakeSourcePolicyAuditRepo) ListByRange(_ context.Context, from, to time.Time, sourceID, mode string) ([]out.SourcePolicyDecisionRecord, error) {
	return f.records, nil
}

func TestAuditSourcePolicies_SummarizesDivergences(t *testing.T) {
	repo := &fakeSourcePolicyAuditRepo{records: []out.SourcePolicyDecisionRecord{
		{ID: "1", SourceID: "sprisotopes", DecisionCode: "official_confirmation_no_publish", WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "shadow"},
		{ID: "2", SourceID: "sprisotopes", DecisionCode: "publish_substantial_update", WouldPublishStandard: true, WouldPublishSpecial: true, Mode: "shadow"},
		{ID: "3", SourceID: "sprisotopes", DecisionCode: "retain_cluster_member", WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "shadow"},
	}}
	report, err := usecase.AuditSourcePolicies(context.Background(), repo, time.Time{}, time.Now(), "", "")
	if err != nil {
		t.Fatalf("AuditSourcePolicies() error = %v", err)
	}
	if report.TotalDecisions != 3 {
		t.Errorf("TotalDecisions = %d, want 3", report.TotalDecisions)
	}
	if report.Divergences != 2 {
		t.Errorf("Divergences = %d, want 2 (records where standard != special)", report.Divergences)
	}
	if report.ByDecisionCode["official_confirmation_no_publish"] != 1 {
		t.Errorf("ByDecisionCode[official_confirmation_no_publish] = %d, want 1", report.ByDecisionCode["official_confirmation_no_publish"])
	}
}

func TestAuditSourcePolicies_BreaksDownByCooldownOutcome(t *testing.T) {
	repo := &fakeSourcePolicyAuditRepo{records: []out.SourcePolicyDecisionRecord{
		{ID: "1", SourceID: "sprisotopes", DecisionCode: "official_confirmation_no_publish", CooldownOutcome: "reserved", WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "enforce"},
		{ID: "2", SourceID: "sprisotopes", DecisionCode: "publish_substantial_update", CooldownOutcome: "forced", WouldPublishStandard: true, WouldPublishSpecial: true, Mode: "enforce"},
		{ID: "3", SourceID: "sprisotopes", DecisionCode: "retain_cluster_member", CooldownOutcome: "", WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "shadow"},
		{ID: "4", SourceID: "sprisotopes", DecisionCode: "retain_cluster_member", CooldownOutcome: "blocked_by_cooldown", WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "enforce"},
	}}
	report, err := usecase.AuditSourcePolicies(context.Background(), repo, time.Time{}, time.Now(), "", "")
	if err != nil {
		t.Fatalf("AuditSourcePolicies() error = %v", err)
	}
	if report.ByCooldownOutcome["reserved"] != 1 {
		t.Errorf("ByCooldownOutcome[reserved] = %d, want 1", report.ByCooldownOutcome["reserved"])
	}
	if report.ByCooldownOutcome["forced"] != 1 {
		t.Errorf("ByCooldownOutcome[forced] = %d, want 1", report.ByCooldownOutcome["forced"])
	}
	if report.ByCooldownOutcome[""] != 1 {
		t.Errorf("ByCooldownOutcome[\"\"] = %d, want 1", report.ByCooldownOutcome[""])
	}
	if report.ByCooldownOutcome["blocked_by_cooldown"] != 1 {
		t.Errorf("ByCooldownOutcome[blocked_by_cooldown] = %d, want 1", report.ByCooldownOutcome["blocked_by_cooldown"])
	}
}
