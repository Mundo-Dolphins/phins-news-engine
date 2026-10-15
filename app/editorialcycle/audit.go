package editorialcycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AuditReport summarizes one editorial cycle run end to end — assembled
// incrementally by cmd/editorialcycle.go's RunE as each step completes,
// written even on early failure (once a session exists) so a partial
// report is available.
type AuditReport struct {
	SessionID            string
	ArticlesReviewed     int
	PublishCount         int
	RejectCount          int
	DictionaryTermsAdded int
	TrainingCandidates   int
	Winner               string // "" if none proposed
	WinnerDeclined       bool
	CIPassed             bool
	Built                bool
	Deployed             bool
	Healthy              bool
	RolledBack           bool
	// Error is non-empty if the run failed partway through, OR if the
	// review paused because clean EOF was reached with candidates still
	// pending — see design spec §8. The latter is a routine, by-design
	// pause, not a failure: Complete's pending-guard refusal is recorded
	// here even though cmd/editorialcycle.go's RunE goes on to call Pause
	// and return nil for it, so an operator scanning audit reports for
	// "did this run leave something undone" doesn't have to separately
	// check the session's status. (The other two pause triggers — an
	// explicit save-and-exit and SIGINT/SIGTERM — do not populate Error on
	// their own success path; only a subsequent failure to even write the
	// "paused" status does.)
	Error string

	// Telegram history import. TelegramImportSkipped is true
	// only when the operator explicitly passed --skip-telegram-import;
	// TelegramImportSkipReason then records why. When it ran instead, the
	// remaining fields mirror application/telegramimport.Result — always
	// populated with whatever was processed before either finishing or
	// aborting, never left zeroed on failure (see Error above, which
	// carries the abort reason).
	TelegramImportSkipped         bool
	TelegramImportSkipReason      string
	TelegramImportProcessed       int
	TelegramImportImported        int
	TelegramImportAlreadyKnown    int
	TelegramImportUnmatched       int
	TelegramImportSkippedMessages int
	TelegramImportFailed          int

	// RosterSync records the nflverse roster prerequisite: "synced:<players>",
	// "skipped:<reason>" or "failed".
	RosterSync string

	// Entity step (spec 2026-10-08-editorial-cycle-entities). EntitiesSkipped is
	// true when the step did not run (flag or configuration), with the reason.
	EntitiesSkipped         bool
	EntitiesSkipReason      string
	EntitiesArticles        int
	EntitiesProposed        int
	EntitiesApproved        int
	EntitiesRejected        int
	EntitiesVerification    string
	EntitiesDiscoveryFailed bool

	// Event rules step (spec 2026-10-09-event-rules-discovery). EventRulesSkipped
	// is true when the step did not run (flag or configuration), with the reason.
	EventRulesSkipped         bool
	EventRulesSkipReason      string
	EventRulesSampled         int
	EventRulesProposed        int
	EventRulesApproved        int
	EventRulesApplied         int
	EventRulesDiscoveryFailed bool
	EventRulesGateBlocked     bool
	// EventRulesGateNotExercised is true when the clustering gate ran but
	// no gold dataset article changed type, action or signal under the
	// candidate, so its pass says nothing about the new rules.
	EventRulesGateNotExercised bool
}

// WriteAuditReport writes report as JSON to
// dir/cycle-<SessionID>-<unix-nano-timestamp>.json (using now to derive
// the timestamp) and returns the path.
func WriteAuditReport(dir string, report AuditReport, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("editorialcycle: create audit dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("cycle-%s-%d.json", report.SessionID, now.UnixNano()))
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("editorialcycle: marshal audit report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("editorialcycle: write audit report %s: %w", path, err)
	}
	return path, nil
}

// MarkdownSummary renders a short human-readable summary for stdout.
func (r AuditReport) MarkdownSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Editorial cycle summary — session %s\n\n", r.SessionID)
	fmt.Fprintf(&b, "- Articles reviewed: %d (publish=%d, reject=%d)\n", r.ArticlesReviewed, r.PublishCount, r.RejectCount)
	fmt.Fprintf(&b, "- Dictionary terms added: %d\n", r.DictionaryTermsAdded)
	fmt.Fprintf(&b, "- Training candidates: %d\n", r.TrainingCandidates)
	// Deliberately worded to avoid the exact phrases the training/promote
	// steps themselves print ("no candidate passed gates", "promoted "),
	// so this summary line — which always appears, even when training or
	// promotion never ran (--skip-train) or was declined — can't be
	// mistaken by a substring check for that step having actually run.
	switch {
	case r.Winner == "":
		b.WriteString("- Winner: none\n")
	case r.WinnerDeclined:
		fmt.Fprintf(&b, "- Winner: %s (human declined to promote)\n", r.Winner)
	default:
		fmt.Fprintf(&b, "- Winner: %s\n", r.Winner)
	}
	fmt.Fprintf(&b, "- CI passed: %v, built: %v, deployed: %v, healthy: %v, rolled back: %v\n", r.CIPassed, r.Built, r.Deployed, r.Healthy, r.RolledBack)
	if r.TelegramImportSkipped {
		fmt.Fprintf(&b, "- Telegram import: SKIPPED (%s)\n", r.TelegramImportSkipReason)
	} else {
		fmt.Fprintf(&b, "- Telegram import: processed=%d imported=%d already_known=%d unmatched=%d skipped=%d failed=%d\n",
			r.TelegramImportProcessed, r.TelegramImportImported, r.TelegramImportAlreadyKnown, r.TelegramImportUnmatched, r.TelegramImportSkippedMessages, r.TelegramImportFailed)
	}
	if r.RosterSync != "" {
		fmt.Fprintf(&b, "- Roster sync: %s\n", r.RosterSync)
	}
	switch {
	case r.EntitiesSkipped:
		fmt.Fprintf(&b, "- Entities: SKIPPED (%s)\n", r.EntitiesSkipReason)
	case r.EntitiesVerification != "":
		fmt.Fprintf(&b, "- Entities: articles=%d proposed=%d approved=%d rejected=%d verification=%s", r.EntitiesArticles, r.EntitiesProposed, r.EntitiesApproved, r.EntitiesRejected, r.EntitiesVerification)
		if r.EntitiesDiscoveryFailed {
			b.WriteString(" (discovery unavailable: continued without entities)")
		}
		b.WriteString("\n")
	}
	switch {
	case r.EventRulesSkipped:
		fmt.Fprintf(&b, "- Event rules: SKIPPED (%s)\n", r.EventRulesSkipReason)
	case r.EventRulesSampled > 0 || r.EventRulesProposed > 0 || r.EventRulesApproved > 0 || r.EventRulesApplied > 0 ||
		r.EventRulesDiscoveryFailed || r.EventRulesGateBlocked || r.EventRulesGateNotExercised:
		fmt.Fprintf(&b, "- Event rules: sampled=%d proposed=%d approved=%d applied=%d", r.EventRulesSampled, r.EventRulesProposed, r.EventRulesApproved, r.EventRulesApplied)
		if r.EventRulesDiscoveryFailed {
			b.WriteString(" (discovery unavailable: continued without new event rules)")
		}
		if r.EventRulesGateBlocked {
			b.WriteString(" (clustering gate blocked the apply)")
		}
		if r.EventRulesGateNotExercised {
			b.WriteString(" (gate not exercised (0 dataset articles affected))")
		}
		b.WriteString("\n")
	}
	if r.Error != "" {
		fmt.Fprintf(&b, "- Error: %s\n", r.Error)
	}
	return b.String()
}
