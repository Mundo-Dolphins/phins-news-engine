// infra/sourcepolicyfile/sourcepolicyfile_test.go
package sourcepolicyfile_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source-policies.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestLoad_ValidFile(t *testing.T) {
	path := writeTemp(t, `
version: 1
mode: shadow
policies:
  - source_id: rss:sprisotopes
    authority: official_team
    retain_near_duplicates: true
    representative_preference: high
    allow_official_confirmation: true
    allow_substantial_update: true
    publish_confirmation_without_new_information: false
    minimum_update_novelty: 0.65
    event_cooldown: 20m
`)
	f, err := sourcepolicyfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if f.Mode != string(sourcepolicy.ModeShadow) {
		t.Errorf("Mode = %q, want %q", f.Mode, sourcepolicy.ModeShadow)
	}
	if len(f.Policies) != 1 {
		t.Fatalf("len(Policies) = %d, want 1", len(f.Policies))
	}
	p := f.Policies[0]
	expectedID := mustID(t, source.TypeRSS, "sprisotopes")
	if p.SourceID != expectedID || p.Authority != "official_team" {
		t.Errorf("unexpected policy entry: %+v", p)
	}
	if p.EventCooldownDuration() != 20*time.Minute {
		t.Errorf("EventCooldownDuration() = %v, want 20m", p.EventCooldownDuration())
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := sourcepolicyfile.Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("Load() error = nil, want error for missing file")
	}
}

func TestLoad_UnknownField_Rejected(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: shadow\nbogus_field: true\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for unknown field")
	}
}

func TestLoad_DuplicateSourceID_Rejected(t *testing.T) {
	path := writeTemp(t, `
version: 1
mode: shadow
policies:
  - source_id: rss:dup
    authority: official_team
  - source_id: rss:dup
    authority: aggregator
`)
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for duplicate source_id")
	}
}

func TestLoad_InvalidAuthority_Rejected(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: shadow\npolicies:\n  - source_id: rss:x\n    authority: super_official\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid authority")
	}
}

func TestLoad_InvalidMode_Rejected(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: yolo\npolicies: []\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid mode")
	}
}

func TestLoad_EmptyMode_DefaultsToDisabled(t *testing.T) {
	path := writeTemp(t, "version: 1\npolicies: []\n")
	f, err := sourcepolicyfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if f.Mode != string(sourcepolicy.ModeDisabled) {
		t.Errorf("Mode = %q, want %q (defaulted)", f.Mode, sourcepolicy.ModeDisabled)
	}
}

func TestLoad_ExactDuplicatesMustBeReject(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: shadow\npolicies:\n  - source_id: rss:x\n    authority: official_team\n    exact_duplicates: allow\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for non-reject exact_duplicates")
	}
}

func TestLoad_NegativeNoveltyThreshold_Rejected(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: shadow\npolicies:\n  - source_id: rss:x\n    authority: official_team\n    minimum_update_novelty: -0.1\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for negative minimum_update_novelty")
	}
}

func TestLoad_NoveltyThresholdAboveOne_Rejected(t *testing.T) {
	path := writeTemp(t, "version: 1\nmode: shadow\npolicies:\n  - source_id: rss:x\n    authority: official_team\n    minimum_update_novelty: 1.5\n")
	if _, err := sourcepolicyfile.Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for minimum_update_novelty > 1")
	}
}
