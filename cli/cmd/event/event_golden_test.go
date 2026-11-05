package event_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// goldenFixture seeds three claims at a fixed time so the output is stable.
func goldenFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	f.now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f.seed(t, "2026-11-01", "failed", "a1", "delivered", "dead")
	f.seed(t, "2026-11-02", "published", "a2", "delivered")
	f.seed(t, "2026-11-03", "claimed", "a3")
	return f
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func TestClaimsListOutputUnchanged(t *testing.T) {
	out, err := goldenFixture(t).run(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "list.txt", out)
}

func TestClaimsShowOutputUnchanged(t *testing.T) {
	out, err := goldenFixture(t).run(t, "show", "injury", "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "show.txt", out)
}

func TestClaimsRetryDryRunOutputUnchanged(t *testing.T) {
	out, err := goldenFixture(t).run(t, "retry", "injury", "2026-11-01", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "retry_dry_run.txt", out)
}
