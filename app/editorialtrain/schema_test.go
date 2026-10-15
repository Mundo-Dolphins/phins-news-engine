package editorialtrain_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func labeledExample(id, title, label string) editorialtrain.TrainingExample {
	return editorialtrain.TrainingExample{ArticleID: id, Title: title, Label: label}
}

func TestBuildSchema_KeepsHighChiSquareFeatureOverNoise(t *testing.T) {
	var examples []editorialtrain.TrainingExample
	// "isotopes" appears only in publish-labeled examples (perfectly correlated).
	for i := 0; i < 10; i++ {
		examples = append(examples, labeledExample("p", "isotopes news update", "publish"))
	}
	for i := 0; i < 10; i++ {
		examples = append(examples, labeledExample("r", "shop sponsored deal", "reject"))
	}
	// "the" appears in every example regardless of label (no signal, and
	// MaxDocFreq should exclude it as too common).
	for i := range examples {
		examples[i].Title = "the " + examples[i].Title
	}
	d := editorialtrain.Dataset{Examples: examples}

	schema := editorialtrain.BuildSchema(d, editorialtrain.PruneConfig{MinDocFreq: 0.02, MaxDocFreq: 0.9, MaxFeatures: 5}, "fs-test")

	found := false
	for _, f := range schema.Features {
		if f == "uni:isotopes" {
			found = true
		}
		if f == "uni:the" {
			t.Errorf("uni:the appears in every doc (df=1.0), should be excluded by MaxDocFreq=0.9")
		}
	}
	if !found {
		t.Errorf("expected uni:isotopes (perfectly label-correlated) in schema, got %v", schema.Features)
	}
	if len(schema.Features) > 5 {
		t.Errorf("len(schema.Features) = %d, want <= MaxFeatures (5)", len(schema.Features))
	}
}

func TestBuildSchema_MinDocFreqExcludesRareFeatures(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		labeledExample("a", "isotopes win big", "publish"),
		labeledExample("b", "isotopes lose close", "reject"),
		labeledExample("c", "onceinalifetimeword appears here", "publish"), // appears in exactly 1/3 examples
	}
	d := editorialtrain.Dataset{Examples: examples}
	schema := editorialtrain.BuildSchema(d, editorialtrain.PruneConfig{MinDocFreq: 0.5, MaxDocFreq: 1.0, MaxFeatures: 0}, "fs-test")

	for _, f := range schema.Features {
		if f == "uni:onceinalifetimeword" {
			t.Error("uni:onceinalifetimeword has df=1/3 < MinDocFreq=0.5, should be excluded")
		}
	}
}

func TestSchemaOverlap_ComputesJaccardAndDelta(t *testing.T) {
	candidate := editorialtrain.FeatureSchema{Features: []string{"uni:a", "uni:b", "uni:c"}}
	promoted := editorialtrain.FeatureSchema{Features: []string{"uni:b", "uni:c", "uni:d"}}

	jaccard, added, removed := editorialtrain.SchemaOverlap(candidate, promoted)

	// intersection {b,c}=2, union {a,b,c,d}=4 -> jaccard 0.5
	if jaccard != 0.5 {
		t.Errorf("jaccard = %v, want 0.5", jaccard)
	}
	if added != 1 { // "uni:a" is new in candidate
		t.Errorf("added = %d, want 1", added)
	}
	if removed != 1 { // "uni:d" is gone from candidate
		t.Errorf("removed = %d, want 1", removed)
	}
}

func TestSchemaOverlap_IdenticalSchemasHaveJaccardOne(t *testing.T) {
	s := editorialtrain.FeatureSchema{Features: []string{"uni:a", "uni:b"}}
	jaccard, added, removed := editorialtrain.SchemaOverlap(s, s)
	if jaccard != 1 || added != 0 || removed != 0 {
		t.Errorf("got jaccard=%v added=%d removed=%d, want 1/0/0", jaccard, added, removed)
	}
}

func TestSchemaOverlap_EmptyPromotedSchemaIsZeroOverlapNotDivideByZero(t *testing.T) {
	candidate := editorialtrain.FeatureSchema{Features: []string{"uni:a"}}
	jaccard, added, removed := editorialtrain.SchemaOverlap(candidate, editorialtrain.FeatureSchema{})
	if jaccard != 0 || added != 1 || removed != 0 {
		t.Errorf("got jaccard=%v added=%d removed=%d, want 0/1/0", jaccard, added, removed)
	}
}

func TestBuildSchema_VersionAndDeterminism(t *testing.T) {
	d := editorialtrain.Dataset{Examples: []editorialtrain.TrainingExample{
		labeledExample("a", "isotopes", "publish"), labeledExample("b", "sponsored", "reject"),
	}}
	cfg := editorialtrain.PruneConfig{MinDocFreq: 0, MaxDocFreq: 1, MaxFeatures: 0}
	s1 := editorialtrain.BuildSchema(d, cfg, "fs-7")
	s2 := editorialtrain.BuildSchema(d, cfg, "fs-7")
	if s1.Version != "fs-7" {
		t.Errorf("Version = %q, want %q", s1.Version, "fs-7")
	}
	if len(s1.Features) != len(s2.Features) {
		t.Fatalf("non-deterministic feature count: %d vs %d", len(s1.Features), len(s2.Features))
	}
	for i := range s1.Features {
		if s1.Features[i] != s2.Features[i] {
			t.Errorf("non-deterministic order at index %d: %q vs %q", i, s1.Features[i], s2.Features[i])
		}
	}
}
