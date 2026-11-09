package entitywiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

type stubResolver struct {
	result out.EntityResolutionResult
	err    error
}

func (s stubResolver) Resolve(context.Context, out.EntityResolutionCandidate) (out.EntityResolutionResult, error) {
	return s.result, s.err
}

func TestExtractNoText(t *testing.T) {
	// A nil config proves the no-text check runs before anything reads it.
	_, _, err := Extract(nil, ExtractInput{Title: "  ", Description: "\n", Body: "\t"}, time.Now(), nil)
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("err = %v, want ErrNoText", err)
	}
}

func TestExtractWithExplicitPathsNeedsNoConfig(t *testing.T) {
	dir := t.TempDir()
	manual := filepath.Join(dir, "manual.yml")
	generated := filepath.Join(dir, "generated.yml")
	body := "entities:\n    - id: jordan-sample\n      canonical_name: Jordan Sample\n      kind: player\n      terms: [Jordan Sample, Sample]\n"
	if err := os.WriteFile(manual, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("entities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := ExtractInput{
		Title:     "Jordan Sample returns to practice",
		RulesPath: repofs.ExampleConfig(t, "event_clustering_rules.yml"), ManualPath: manual, GeneratedPath: generated,
	}
	if NeedsConfig(in) {
		t.Fatal("NeedsConfig = true with all three paths given")
	}
	rep, warnings, err := Extract(nil, in, time.Now(), nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if rep.PrimaryEntity != "Jordan Sample" {
		t.Fatalf("PrimaryEntity = %q, want Jordan Sample", rep.PrimaryEntity)
	}
}

func TestExtractNeedsConfigRule(t *testing.T) {
	full := ExtractInput{RulesPath: "r", ManualPath: "m", GeneratedPath: "g"}
	if NeedsConfig(full) {
		t.Fatal("full paths must not need config")
	}
	full.League = true
	if !NeedsConfig(full) {
		t.Fatal("league without roster paths needs config")
	}
	full.RosterPath, full.LeagueRulesPath = "a", "b"
	if NeedsConfig(full) {
		t.Fatal("league with both paths must not need config")
	}
	if !NeedsConfig(ExtractInput{}) {
		t.Fatal("empty input needs config")
	}
}

func TestFallbackTestValidationRejected(t *testing.T) {
	fb := config.EntityFallbackConfig{Provider: "groq", MinConfidence: 0.9}
	r := FallbackTest(context.Background(),
		stubResolver{result: out.EntityResolutionResult{Resolved: true, Type: "player", CanonicalName: "Jordan Sample", Confidence: 0.5}},
		fb, "Jordan Sample", "Jordan Sample signs", "title")
	if r.Err != nil || r.ValidateErr == nil {
		t.Fatalf("Err = %v, ValidateErr = %v, want only ValidateErr", r.Err, r.ValidateErr)
	}
	if r.Provider != "groq" {
		t.Fatalf("Provider = %q", r.Provider)
	}
}

func TestFallbackTestProviderErrorAndUnknownProvider(t *testing.T) {
	boom := errors.New("boom")
	r := FallbackTest(context.Background(), stubResolver{err: boom}, config.EntityFallbackConfig{}, "x", "y", "title")
	if !errors.Is(r.Err, boom) || r.Provider != "unknown" {
		t.Fatalf("report = %+v", r)
	}
}

func TestSyncRosterNoPath(t *testing.T) {
	_, _, err := SyncRoster(context.Background(), config.RosterConfig{}, "", "", time.Now())
	if err == nil || !strings.Contains(err.Error(), "no roster path") {
		t.Fatalf("err = %v, want no roster path", err)
	}
}
