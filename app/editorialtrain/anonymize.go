package editorialtrain

import (
	"crypto/sha256"
	"encoding/hex"
)

// AnonymizeIDs returns a salted, truncated hash of each id in ids, salted
// with datasetHash — stable within one dataset/report (a reader can
// still tell "these two false positives are the same article" across a
// report's sections) but not reversible to the real article ID without
// the dataset file itself. Used only when rendering a human-facing
// report (see the design doc §4); internal consumers (gates,
// metrics.json) keep real IDs. A nil/empty ids returns an empty (not
// nil) slice.
func AnonymizeIDs(ids []string, datasetHash string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		sum := sha256.Sum256([]byte(datasetHash + ":" + id))
		out = append(out, hex.EncodeToString(sum[:])[:12])
	}
	return out
}
