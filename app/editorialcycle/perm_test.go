package editorialcycle_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

func TestWriteAuditReport_UsesPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	path, err := editorialcycle.WriteAuditReport(dir, editorialcycle.AuditReport{SessionID: "s1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, dir, 0o700)
	fsperm.Assert(t, path, 0o600)
}
