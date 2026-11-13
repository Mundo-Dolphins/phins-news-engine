package editorial_test

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

var (
	goldenSessionID = regexp.MustCompile(`cycle-\d+`)
	goldenPublished = regexp.MustCompile(`Published: \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC`)
	goldenTempPath  = regexp.MustCompile(`\S*/audit/\S+\.json`)
)

// TestCycleReviewGolden_SaveAndExitPauses pins the cycle's text from the start
// of the session to the pause after save-and-exit: the session line, the
// review screen and the pause message, with the session id, the publication
// time and the audit path normalised.
func TestCycleReviewGolden_SaveAndExitPauses(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	seedReviewCandidate(t, filepath.Join(filepath.Dir(configPath), "operational.db"), "rg1")
	args := append([]string{"--config", configPath, "editorial", "cycle"}, append(baseCycleArgs, "--skip-entities", "--skip-dictionary")...)
	out, err := testsupport.ExecuteWithStdin(t, "w\n", args...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	got := goldenSessionID.ReplaceAllString(out, "cycle-ID")
	got = goldenPublished.ReplaceAllString(got, "Published: DATE")
	got = goldenTempPath.ReplaceAllString(got, "AUDIT")
	want := "WARNING: --skip-telegram-import is set — this session's publications will be reconstructed without Telegram history. Only use this when Telegram credentials are genuinely unavailable.\n" +
		"started session cycle-ID\n" +
		"================================================================\n" +
		">>> rg1 [1/1] Isotopes sign veteran CB\n" +
		"----------------------------------------------------------------\n" +
		"ID:           rg1\n" +
		"Source:       Test Feed (rss)   Published: DATE\n" +
		"URL:          https://example.com/rg1\n" +
		"Cluster:       (role: )\n" +
		"Summary:      \n" +
		"Score:        3.00 (ambiguity signal 0.00)\n" +
		"Prior label:  (none)\n" +
		"Similar:      0 other article(s) in this cluster\n" +
		"----------------------------------------------------------------\n" +
		"[p]ublish [r]eject [a]mbiguous [d]uplicate [s]ame-news [u]pdate [e]dit-category\n" +
		"[b]ack [k]skip [w]save-and-exit\n" +
		"> session saved — resume with --resume\n" +
		"audit report: AUDIT\n" +
		"# Editorial cycle summary — session cycle-ID\n" +
		"\n" +
		"- Articles reviewed: 1 (publish=0, reject=0)\n" +
		"- Dictionary terms added: 0\n" +
		"- Training candidates: 0\n" +
		"- Winner: none\n" +
		"- CI passed: false, built: false, deployed: false, healthy: false, rolled back: false\n" +
		"- Telegram import: SKIPPED (explicit --skip-telegram-import flag)\n" +
		"- Roster sync: skipped:entity step off or roster disabled\n"
	if got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
}
