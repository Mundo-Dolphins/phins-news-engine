package engine_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEngineNeverDependsOnHostWiring keeps the engine generic: a Telegram
// MTProto (gotd) history client and any host-specific fetcher wiring
// are injected by the host binary through Options, never imported here.
//
// It also carries over the old production-binary import guard: the
// runtime service never talks to a knowledge source (Wikidata and its
// cache are offline-tool dependencies only).
func TestEngineNeverDependsOnHostWiring(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := string(out)
	if !strings.Contains(deps, "/infra/fetcherwiring") {
		t.Fatalf("go list -deps output does not look right:\n%s", deps)
	}
	for _, forbidden := range []string{
		"github.com/gotd/td",
		"/infra/wikidata",
		"/app/knowledgecache",
		"/adapters/outbound/sqliteknowledgecache",
	} {
		if strings.Contains(deps, forbidden) {
			t.Errorf("engine depends on %s; it must be injected by the host via Options", forbidden)
		}
	}
}
