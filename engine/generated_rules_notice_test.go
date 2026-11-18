// engine/generated_rules_notice_test.go
package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedRulesNotice(t *testing.T) {
	dir := t.TempDir()
	missing, merged, _, err := generatedRulesNotice(filepath.Join(dir, "typo.yml"), nil)
	if err != nil || !missing || merged != 0 {
		t.Fatalf("missing file: missing=%v merged=%d err=%v; want a warning-worthy missing file", missing, merged, err)
	}
	path := filepath.Join(dir, "generated.yml")
	body := "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: v1}\n    - {phrase: signs, proposal_id: evr_2, prompt_version: v1}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// One phrase was skipped by the merge (it duplicates a hand-written one).
	missing, merged, types, err := generatedRulesNotice(path, []string{`generated phrase "signs" for action_terms.sign skipped: already in action_terms.sign`})
	if err != nil || missing || merged != 1 || types != 0 {
		t.Fatalf("missing=%v merged=%d types=%d err=%v; want 1 merged phrase, 0 types", missing, merged, types, err)
	}
}

func TestGeneratedRulesNotice_InertTypeWarningKeepsPhraseCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.yml")
	body := "meta: {version: 1}\nevent_types:\n  - {name: kicking-error, window: 48h, proposal_id: evr_3}\n  - {name: quiet-type, window: 48h, proposal_id: evr_4}\naction_terms:\n  kicking-error:\n    - {phrase: misses a kick, proposal_id: evr_5, prompt_version: v1}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, merged, types, err := generatedRulesNotice(path, []string{`generated event type "quiet-type" has no phrase; it is inert`})
	if err != nil || missing || merged != 1 || types != 2 {
		t.Fatalf("missing=%v merged=%d types=%d err=%v; want 1 phrase, 2 types", missing, merged, types, err)
	}
}
