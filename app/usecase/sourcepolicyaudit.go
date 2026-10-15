package usecase

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SourcePolicyAuditReport summarizes source-policy decisions over a date
// range — what `source-policy audit` prints, and what a shadow-mode
// promotion review reads before flipping mode to enforce.
type SourcePolicyAuditReport struct {
	TotalDecisions int
	Divergences    int // records where WouldPublishStandard != WouldPublishSpecial
	ByDecisionCode map[string]int
	// ByCooldownOutcome breaks decisions down by CooldownOutcome — the
	// differentiated shadow-vs-enforce field: "" (not
	// applicable), "shadow", "reserved", "forced", or
	// "blocked_by_cooldown".
	ByCooldownOutcome map[string]int
	BySource          map[string]int
}

// AuditSourcePolicies queries repo for every record in [from, to),
// optionally filtered by sourceID/mode (either "" skips that filter), and
// summarizes it.
func AuditSourcePolicies(ctx context.Context, repo out.SourcePolicyDecisionRepository, from, to time.Time, sourceID, mode string) (SourcePolicyAuditReport, error) {
	records, err := repo.ListByRange(ctx, from, to, sourceID, mode)
	if err != nil {
		return SourcePolicyAuditReport{}, err
	}
	report := SourcePolicyAuditReport{
		ByDecisionCode:    make(map[string]int),
		ByCooldownOutcome: make(map[string]int),
		BySource:          make(map[string]int),
	}
	for _, r := range records {
		report.TotalDecisions++
		if r.WouldPublishStandard != r.WouldPublishSpecial {
			report.Divergences++
		}
		report.ByDecisionCode[r.DecisionCode]++
		report.ByCooldownOutcome[r.CooldownOutcome]++
		report.BySource[r.SourceID]++
	}
	return report, nil
}
