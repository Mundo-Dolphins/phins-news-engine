// app/eventrulecycle/gate.go
package eventrulecycle

import (
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
)

// gateEps absorbs floating-point noise when comparing metrics.
const gateEps = 1e-9

// GateResult is the verdict of the clustering regression gate.
type GateResult struct {
	OK     bool
	Reason string
	Before clustereval.Report
	After  clustereval.Report
	// Affected is the number of dataset articles whose event type, action
	// or content signal differs between the baseline and the candidate.
	// Zero means the gate passed without being exercised.
	Affected int
}

// EvaluateGate passes when the candidate rules do not lower pairwise
// precision or purity and do not add over-grouped clusters.
func EvaluateGate(before, after clustereval.Report) GateResult {
	res := GateResult{Before: before, After: after}
	var reasons []string
	if bp, ap := before.Pairwise.Precision(), after.Pairwise.Precision(); ap < bp-gateEps {
		reasons = append(reasons, fmt.Sprintf("pairwise precision dropped from %.4f to %.4f", bp, ap))
	}
	if after.Purity < before.Purity-gateEps {
		reasons = append(reasons, fmt.Sprintf("purity dropped from %.4f to %.4f", before.Purity, after.Purity))
	}
	if after.OverGroupedClusters > before.OverGroupedClusters {
		reasons = append(reasons, fmt.Sprintf("over-grouped clusters rose from %d to %d", before.OverGroupedClusters, after.OverGroupedClusters))
	}
	res.OK = len(reasons) == 0
	res.Reason = strings.Join(reasons, "; ")
	return res
}
