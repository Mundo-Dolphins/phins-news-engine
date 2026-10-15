package editorialcycle_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
)

func TestWriteAuditReport_WritesJSONAndReturnsPath(t *testing.T) {
	dir := t.TempDir()
	report := editorialcycle.AuditReport{
		SessionID: "s1", ArticlesReviewed: 3, DictionaryTermsAdded: 1, TrainingCandidates: 2,
		Winner: "naive_bayes-candidate-1", CIPassed: true, Deployed: true, Healthy: true,
	}
	path, err := editorialcycle.WriteAuditReport(dir, report, time.Now())
	if err != nil {
		t.Fatalf("WriteAuditReport() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written report: %v", err)
	}
	var roundTripped editorialcycle.AuditReport
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal written report: %v", err)
	}
	if roundTripped.SessionID != "s1" || roundTripped.ArticlesReviewed != 3 {
		t.Errorf("roundTripped = %+v, want it to match the input report", roundTripped)
	}
}

func TestAuditReport_MarkdownSummary_MentionsKeyOutcomes(t *testing.T) {
	report := editorialcycle.AuditReport{SessionID: "s1", ArticlesReviewed: 3, Winner: "naive_bayes-candidate-1", Deployed: true, Healthy: true}
	md := report.MarkdownSummary()
	for _, want := range []string{"s1", "3", "naive_bayes-candidate-1", "deployed"} {
		if !strings.Contains(md, want) {
			t.Errorf("MarkdownSummary() = %q, want it to mention %q", md, want)
		}
	}
}

func TestAuditReport_MarkdownSummaryIncludesTheEntityStep(t *testing.T) {
	r := editorialcycle.AuditReport{
		SessionID: "s1", RosterSync: "synced:1864",
		EntitiesArticles: 12, EntitiesProposed: 9, EntitiesApproved: 4, EntitiesRejected: 3, EntitiesVerification: "local",
	}
	got := r.MarkdownSummary()
	for _, want := range []string{"Roster sync: synced:1864", "Entities: articles=12 proposed=9 approved=4 rejected=3 verification=local"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	skipped := editorialcycle.AuditReport{SessionID: "s2", EntitiesSkipped: true, EntitiesSkipReason: "explicit --skip-entities flag"}.MarkdownSummary()
	if !strings.Contains(skipped, "Entities: SKIPPED (explicit --skip-entities flag)") {
		t.Errorf("skipped summary:\n%s", skipped)
	}
	failed := editorialcycle.AuditReport{SessionID: "s3", EntitiesDiscoveryFailed: true, EntitiesVerification: "local"}.MarkdownSummary()
	if !strings.Contains(failed, "discovery unavailable") {
		t.Errorf("a discovery outage must be visible in the summary:\n%s", failed)
	}
	none := editorialcycle.AuditReport{SessionID: "s4"}.MarkdownSummary()
	if strings.Contains(none, "Roster sync") || strings.Contains(none, "Entities:") {
		t.Errorf("a report from before the entity step must not print entity lines:\n%s", none)
	}
}

func TestAuditReport_MarkdownSummaryIncludesTheEventRulesStep(t *testing.T) {
	ran := editorialcycle.AuditReport{
		SessionID: "s1", EventRulesSampled: 40, EventRulesProposed: 7, EventRulesApproved: 2, EventRulesApplied: 2,
	}.MarkdownSummary()
	if !strings.Contains(ran, "Event rules: sampled=40 proposed=7 approved=2 applied=2") {
		t.Errorf("summary lacks the event rules line:\n%s", ran)
	}
	skipped := editorialcycle.AuditReport{SessionID: "s2", EventRulesSkipped: true, EventRulesSkipReason: "explicit --skip-event-rules flag"}.MarkdownSummary()
	if !strings.Contains(skipped, "Event rules: SKIPPED (explicit --skip-event-rules flag)") {
		t.Errorf("skipped summary:\n%s", skipped)
	}
	failed := editorialcycle.AuditReport{SessionID: "s3", EventRulesSampled: 10, EventRulesDiscoveryFailed: true}.MarkdownSummary()
	if !strings.Contains(failed, "discovery unavailable") {
		t.Errorf("a discovery outage must be visible in the summary:\n%s", failed)
	}
	blocked := editorialcycle.AuditReport{SessionID: "s4", EventRulesSampled: 10, EventRulesApproved: 1, EventRulesGateBlocked: true}.MarkdownSummary()
	if !strings.Contains(blocked, "clustering gate blocked") {
		t.Errorf("a blocked gate must be visible in the summary:\n%s", blocked)
	}
	none := editorialcycle.AuditReport{SessionID: "s5"}.MarkdownSummary()
	if strings.Contains(none, "Event rules:") {
		t.Errorf("a report from before the event rules step must not print its line:\n%s", none)
	}
}

func TestAuditReport_MarkdownSummaryFlagsAVacuousEventRulesGate(t *testing.T) {
	got := editorialcycle.AuditReport{SessionID: "s5", EventRulesSampled: 10, EventRulesApproved: 1, EventRulesApplied: 1, EventRulesGateNotExercised: true}.MarkdownSummary()
	if !strings.Contains(got, "gate not exercised (0 dataset articles affected)") {
		t.Fatalf("summary:\n%s", got)
	}
}
