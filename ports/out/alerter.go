package out

import "context"

// AlertSeverity ranks an Alert for the operator.
type AlertSeverity string

const (
	AlertInfo     AlertSeverity = "info"
	AlertWarning  AlertSeverity = "warning"
	AlertCritical AlertSeverity = "critical"
)

// Alert is an internal notice for the operator (ADR 0037): something failed
// that nobody else will notice. It is not news and never enters the article
// pipeline or the outbox.
type Alert struct {
	// Source names who raised it: an event handler name, or "outbox".
	Source   string
	Severity AlertSeverity
	// Title is one line; Details is free text. Both are redacted and
	// truncated by the Alerter, so callers may pass raw error text.
	Title   string
	Details string
	// DedupKey makes repeats within the Alerter's window collapse into the
	// first one. Empty means no deduplication.
	DedupKey string
}

// Alerter delivers an Alert to the operator. A failure to deliver is returned
// but is never a reason to fail the work that raised the alert: callers log
// it and carry on.
type Alerter interface {
	Alert(ctx context.Context, a Alert) error
}
