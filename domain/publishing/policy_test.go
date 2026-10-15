package publishing_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

func TestEvaluate_AllowsWhenNothingExceeded(t *testing.T) {
	cfg := publishing.PolicyConfig{BurstMaxPerWindow: 6, BurstWindow: time.Hour, SameTopicMaxPerWindow: 2, SameTopicWindow: 3 * time.Hour}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PrimaryEntity: "Jane Doe", PublishedInBurstWindow: 1, PublishedForEntityInWindow: 0}, cfg, now)
	if !decision.Allow {
		t.Errorf("decision = %+v, want Allow=true", decision)
	}
}

func TestEvaluate_BurstLimitDefersUntilWindowEnd(t *testing.T) {
	cfg := publishing.PolicyConfig{BurstMaxPerWindow: 6, BurstWindow: time.Hour, SameTopicMaxPerWindow: 2, SameTopicWindow: 3 * time.Hour}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PublishedInBurstWindow: 6}, cfg, now)
	if decision.Allow {
		t.Fatal("decision.Allow = true, want false: burst limit reached")
	}
	if decision.Reason != publishing.ReasonBurstLimit {
		t.Errorf("Reason = %q, want %q", decision.Reason, publishing.ReasonBurstLimit)
	}
	if !decision.RetryAt.Equal(now.Add(time.Hour)) {
		t.Errorf("RetryAt = %v, want %v", decision.RetryAt, now.Add(time.Hour))
	}
}

func TestEvaluate_SameTopicLimitDefersUntilWindowEnd(t *testing.T) {
	cfg := publishing.PolicyConfig{BurstMaxPerWindow: 6, BurstWindow: time.Hour, SameTopicMaxPerWindow: 2, SameTopicWindow: 3 * time.Hour}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PrimaryEntity: "Jane Doe", PublishedForEntityInWindow: 2}, cfg, now)
	if decision.Allow {
		t.Fatal("decision.Allow = true, want false: same-topic limit reached")
	}
	if decision.Reason != publishing.ReasonSameTopicLimit {
		t.Errorf("Reason = %q, want %q", decision.Reason, publishing.ReasonSameTopicLimit)
	}
	if !decision.RetryAt.Equal(now.Add(3 * time.Hour)) {
		t.Errorf("RetryAt = %v, want %v", decision.RetryAt, now.Add(3*time.Hour))
	}
}

func TestEvaluate_SameTopicLimitIgnoredWhenNoPrimaryEntity(t *testing.T) {
	cfg := publishing.PolicyConfig{SameTopicMaxPerWindow: 1, SameTopicWindow: time.Hour}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PrimaryEntity: "", PublishedForEntityInWindow: 99}, cfg, now)
	if !decision.Allow {
		t.Errorf("decision = %+v, want Allow=true: no primary entity means no same-topic check", decision)
	}
}

func TestEvaluate_SameTopicLimitIgnoredForExemptEntity(t *testing.T) {
	cfg := publishing.PolicyConfig{SameTopicMaxPerWindow: 1, SameTopicWindow: time.Hour, SameTopicExemptEntities: []string{"Springfield Isotopes"}}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PrimaryEntity: "springfield isotopes", PublishedForEntityInWindow: 99}, cfg, now)
	if !decision.Allow {
		t.Errorf("decision = %+v, want Allow=true: exempt entity (case-insensitive) skips the same-topic check", decision)
	}
}

func TestEvaluate_SameTopicExemptionStillAppliesBurstLimit(t *testing.T) {
	cfg := publishing.PolicyConfig{BurstMaxPerWindow: 6, BurstWindow: time.Hour, SameTopicMaxPerWindow: 1, SameTopicWindow: time.Hour, SameTopicExemptEntities: []string{"Springfield Isotopes"}}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{PrimaryEntity: "Springfield Isotopes", PublishedInBurstWindow: 6}, cfg, now)
	if decision.Allow || decision.Reason != publishing.ReasonBurstLimit {
		t.Errorf("decision = %+v, want burst_limit deferral", decision)
	}
}

func TestEvaluate_QuietHoursDefersToWindowEndSameDay(t *testing.T) {
	cfg := publishing.PolicyConfig{QuietHours: publishing.QuietHoursConfig{Enabled: true, StartHour: 2, EndHour: 6}}
	now := time.Date(2026, 7, 16, 3, 30, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{}, cfg, now)
	if decision.Allow {
		t.Fatal("decision.Allow = true, want false: 03:30 is within [02:00,06:00)")
	}
	if decision.Reason != publishing.ReasonQuietHours {
		t.Errorf("Reason = %q, want %q", decision.Reason, publishing.ReasonQuietHours)
	}
	want := time.Date(2026, 7, 16, 6, 0, 0, 0, time.UTC)
	if !decision.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", decision.RetryAt, want)
	}
}

func TestEvaluate_QuietHoursWrapsPastMidnight(t *testing.T) {
	cfg := publishing.PolicyConfig{QuietHours: publishing.QuietHoursConfig{Enabled: true, StartHour: 22, EndHour: 6}}
	now := time.Date(2026, 7, 16, 23, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{}, cfg, now)
	if decision.Allow {
		t.Fatal("decision.Allow = true, want false: 23:00 is within the 22->6 wraparound window")
	}
	want := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	if !decision.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v (tomorrow's 06:00, since 23:00 is past today's)", decision.RetryAt, want)
	}
}

func TestEvaluate_QuietHoursDisabledNeverDefers(t *testing.T) {
	cfg := publishing.PolicyConfig{QuietHours: publishing.QuietHoursConfig{Enabled: false, StartHour: 2, EndHour: 6}}
	now := time.Date(2026, 7, 16, 3, 30, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{}, cfg, now)
	if !decision.Allow {
		t.Errorf("decision = %+v, want Allow=true: quiet hours disabled", decision)
	}
}

func TestEvaluate_OutsideQuietHoursAllows(t *testing.T) {
	cfg := publishing.PolicyConfig{QuietHours: publishing.QuietHoursConfig{Enabled: true, StartHour: 2, EndHour: 6}}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decision := publishing.Evaluate(publishing.PolicyInput{}, cfg, now)
	if !decision.Allow {
		t.Errorf("decision = %+v, want Allow=true: noon is outside [02:00,06:00)", decision)
	}
}
