package editorialtrain

import "github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"

// Trainer is the common interface every candidate algorithm implements.
// Fit sees only pruned, schema-restricted feature vectors (via
// TrainingExample.Features, filtered to schema.Features) — never raw
// text — and returns a Model with Bias/Weights/Algorithm/
// FeatureSchemaVersion populated; Version and Thresholds are the
// orchestrator's responsibility (Task 18), not the Trainer's, since
// neither is knowable from a single Fit call in isolation.
type Trainer interface {
	Algorithm() string
	Fit(examples []TrainingExample, schema FeatureSchema, seed int64) (editorial.Model, error)
}

// restrictToSchema filters fv down to only the features schema knows
// about — every Trainer applies this before optimizing, so a candidate
// model's Weights map never contains a key outside the promoted schema.
func restrictToSchema(fv editorial.FeatureVector, schema FeatureSchema) editorial.FeatureVector {
	out := make(editorial.FeatureVector, len(schema.Features))
	for _, f := range schema.Features {
		if v, ok := fv[f]; ok {
			out[f] = v
		}
	}
	return out
}
