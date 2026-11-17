// infra/clusteringrules/taxonomy_equivalence_test.go
package clusteringrules_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
)

// TestExampleTaxonomyEqualsBuiltIn pins the example config's taxonomy
// section to the reference taxonomy the engine used to hard-code, so
// moving the taxonomy into YAML changes no classification.
func TestExampleTaxonomyEqualsBuiltIn(t *testing.T) {
	f, err := clusteringrules.Load("../../configs/example/event_clustering_rules.yml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := eventsignalstest.Taxonomy()
	if !reflect.DeepEqual(f.Taxonomy, want) {
		t.Fatalf("example taxonomy differs from the reference:\n got %#v\nwant %#v", f.Taxonomy, want)
	}
	if got := f.ToEventSignalsRules().Taxonomy; !reflect.DeepEqual(got, want) {
		t.Error("ToEventSignalsRules().Taxonomy differs from the reference")
	}
	if got := f.ToClusteringConfig().Taxonomy; !reflect.DeepEqual(got, want) {
		t.Error("ToClusteringConfig().Taxonomy differs from the reference")
	}
	if got := f.ToRepresentativeConfig().Taxonomy; !reflect.DeepEqual(got, want) {
		t.Error("ToRepresentativeConfig().Taxonomy differs from the reference")
	}
}
