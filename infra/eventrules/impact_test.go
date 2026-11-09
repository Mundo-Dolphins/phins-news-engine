// infra/eventrules/impact_test.go
package eventrules

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

const impactGenerated = "meta: {version: 1}\naction_terms:\n  sign:\n    - {phrase: zzqx pens a pact, proposal_id: evr_1, prompt_version: event-rules-v1}\ncontent_cues:\n  rumor:\n    - {phrase: zzqx whispers, proposal_id: evr_2, prompt_version: event-rules-v1}\n"

func TestImpactCountsMovedAndRetyped(t *testing.T) {
	db := openTestDB(t)
	cfg := enabledConfig(t)
	cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "generated.yml")
	if err := os.WriteFile(cfg.Clustering.GeneratedRulesPath, []byte(impactGenerated), 0o600); err != nil {
		t.Fatal(err)
	}
	seedArticles(t, db, "g", eventsignals.EventTypeGeneric, "Isotopes zzqx pens a pact with veteran", "Weather in Springfield is sunny")
	seedArticles(t, db, "t", eventsignals.EventTypeTransaction, "Isotopes sign veteran linebacker amid zzqx whispers")

	rep, err := Impact(context.Background(), db, cfg, 7, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.GeneratedPath != cfg.Clustering.GeneratedRulesPath || rep.GeneratedMissing || rep.Days != 7 {
		t.Fatalf("report header = %+v", rep)
	}
	if rep.Generic != 2 || rep.Changed != 1 || rep.Typed != 1 || rep.Retyped != 1 || rep.GenericTruncated || rep.TypedTruncated {
		t.Fatalf("counts = %+v", rep)
	}
	if want := map[string][]string{"transaction": {"Isotopes zzqx pens a pact with veteran"}}; !reflect.DeepEqual(rep.Moved, want) {
		t.Fatalf("Moved = %v, want %v", rep.Moved, want)
	}
	if want := map[string][]string{"transaction -> rumor": {"Isotopes sign veteran linebacker amid zzqx whispers"}}; !reflect.DeepEqual(rep.RetypedBy, want) {
		t.Fatalf("RetypedBy = %v, want %v", rep.RetypedBy, want)
	}
}

func TestImpactMissingGeneratedFileChangesNothing(t *testing.T) {
	db := openTestDB(t)
	cfg := enabledConfig(t)
	cfg.Clustering.GeneratedRulesPath = filepath.Join(t.TempDir(), "absent.yml")
	seedArticles(t, db, "g", eventsignals.EventTypeGeneric, "Isotopes zzqx pens a pact with veteran")
	rep, err := Impact(context.Background(), db, cfg, 7, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.GeneratedMissing || rep.Changed != 0 || rep.Retyped != 0 || rep.Generic != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImpactDaysMustBePositive(t *testing.T) {
	db := openTestDB(t)
	cfg := enabledConfig(t)
	for _, days := range []int{0, -3} {
		if _, err := Impact(context.Background(), db, cfg, days, time.Now()); err == nil || err.Error() != "--days must be positive" {
			t.Errorf("days %d: err = %v", days, err)
		}
	}
}

func TestImpactNeedsRulesPath(t *testing.T) {
	db := openTestDB(t)
	cfg := enabledConfig(t)
	cfg.Clustering.RulesPath = ""
	if _, err := Impact(context.Background(), db, cfg, 7, time.Now()); err == nil || !strings.Contains(err.Error(), "clustering.rules_path is empty") {
		t.Fatalf("err = %v", err)
	}
}
