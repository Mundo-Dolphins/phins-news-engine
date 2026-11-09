package dictionary_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

// TestDictionaryRollbackOutput pins the exact `dictionary rollback` output for
// a dry run, a declined prompt and a forced restore.
func TestDictionaryRollbackOutput(t *testing.T) {
	const diff = "Possible departures (1):\n  - some new entity: absent for 1 consecutive successful run(s) over 0 day(s)\nUnchanged: 1\n"
	cases := []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"dry run", "", []string{"--to", "1.0.0", "--dry-run"}, diff + "dry-run: no changes written\n"},
		{"declined", "n\n", []string{"--to", "1.0.0"}, diff + "restore this content as a new version? [y/N] aborted, no changes written\n"},
		{"forced", "", []string{"--previous", "--force"}, diff + "dictionary rolled back: 1.0.1 -> 1.0.2 (content restored from v1.0.0)\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath, dictPath := seedRollbackFixture(t, dir)
			t.Chdir(dir)
			cfgPath := writeRollbackTestConfig(t, dir, dbPath, dictPath)
			args := append([]string{"--config", cfgPath, "dictionary", "rollback"}, tc.args...)
			out, err := testsupport.ExecuteWithStdin(t, tc.stdin, args...)
			if err != nil {
				t.Fatalf("Execute() error = %v, output: %s", err, out)
			}
			if out != tc.want {
				t.Errorf("output =\n%q\nwant\n%q", out, tc.want)
			}
		})
	}
}
