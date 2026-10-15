// cli/cmd/entities/fallbacktest_test.go
package entities

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeFallbackResolver struct {
	result appout.EntityResolutionResult
	err    error
}

func (f *fakeFallbackResolver) Resolve(context.Context, appout.EntityResolutionCandidate) (appout.EntityResolutionResult, error) {
	return f.result, f.err
}

func TestRunFallbackTest_ResolvedAndValid(t *testing.T) {
	resolver := &fakeFallbackResolver{result: appout.EntityResolutionResult{
		Resolved: true, Type: "player", CanonicalName: "Jai'Rene' Mossbank", Confidence: 0.98,
	}}
	fb := config.EntityFallbackConfig{Provider: "groq", MinConfidence: 0.7}
	var buf bytes.Buffer

	err := runFallbackTest(context.Background(), &buf, resolver, fb,
		"Jai'Rene' Mossbank", "Isotopes claim former Cypress CB Jai'Rene' Mossbank", "title")
	if err != nil {
		t.Fatalf("runFallbackTest() error = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Provider: groq", "Resolved: true", "Type: player", "Canonical name: Jai'Rene' Mossbank", "Confidence: 0.98", "Local validation: accepted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestRunFallbackTest_RejectedByLocalValidation(t *testing.T) {
	resolver := &fakeFallbackResolver{result: appout.EntityResolutionResult{
		Resolved: true, Type: "player", CanonicalName: "Tom Bront", Confidence: 0.95,
	}}
	fb := config.EntityFallbackConfig{Provider: "groq", MinConfidence: 0.7}
	var buf bytes.Buffer

	err := runFallbackTest(context.Background(), &buf, resolver, fb, "Random Newcomer", "Isotopes sign Random Newcomer", "title")
	if err != nil {
		t.Fatalf("runFallbackTest() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Local validation: REJECTED") {
		t.Fatalf("output = %q, want a REJECTED local validation line (hallucinated canonical name)", out)
	}
}

func TestRunFallbackTest_ResolverErrorIsReportedNotReturned(t *testing.T) {
	resolver := &fakeFallbackResolver{err: errors.New("provider unavailable")}
	fb := config.EntityFallbackConfig{Provider: "groq", MinConfidence: 0.7}
	var buf bytes.Buffer

	err := runFallbackTest(context.Background(), &buf, resolver, fb, "Random Newcomer", "x", "title")
	if err != nil {
		t.Fatalf("runFallbackTest() error = %v, want nil (a provider failure is reported in output, not a command error)", err)
	}
	if !strings.Contains(buf.String(), "Error: provider unavailable") {
		t.Fatalf("output = %q, want it to report the provider error", buf.String())
	}
}

func TestRunFallbackTest_UnknownProviderLabel(t *testing.T) {
	resolver := &fakeFallbackResolver{result: appout.EntityResolutionResult{Resolved: false}}
	fb := config.EntityFallbackConfig{MinConfidence: 0.7} // Provider left empty
	var buf bytes.Buffer

	if err := runFallbackTest(context.Background(), &buf, resolver, fb, "X Y", "x", "title"); err != nil {
		t.Fatalf("runFallbackTest() error = %v", err)
	}
	if !strings.Contains(buf.String(), "Provider: unknown") {
		t.Fatalf("output = %q, want \"Provider: unknown\" when fb.Provider is empty", buf.String())
	}
}
