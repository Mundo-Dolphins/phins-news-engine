package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// maxAlertReasonRunes is how much of an error text goes into a dedup key, so
// "the same reason" survives ids and timestamps later in the message.
const maxAlertReasonRunes = 80

// sendAlert delivers a through alerter. A nil alerter does nothing and a
// failed delivery is only logged: an alert must never fail the work that
// raised it (ADR 0037).
func sendAlert(ctx context.Context, alerter out.Alerter, logger out.Logger, a out.Alert) {
	if alerter == nil {
		return
	}
	if err := alerter.Alert(ctx, a); err != nil {
		logger.Error("alert delivery failed", err, "source", a.Source, "dedup_key", a.DedupKey)
	}
}

// reasonKey shortens an error text into a stable dedup component.
func reasonKey(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxAlertReasonRunes {
		s = string(r[:maxAlertReasonRunes])
	}
	return s
}

// EventAlertAdapter lets event handlers raise alerts (out.EventAlertSink)
// through an out.Alerter: one alert per handler and message within the
// Alerter's dedup window.
type EventAlertAdapter struct {
	alerter out.Alerter
	logger  out.Logger
}

// NewEventAlertAdapter wraps alerter, which must not be nil.
func NewEventAlertAdapter(alerter out.Alerter, logger out.Logger) *EventAlertAdapter {
	return &EventAlertAdapter{alerter: alerter, logger: logger}
}

var _ out.EventAlertSink = (*EventAlertAdapter)(nil)

// Alert implements out.EventAlertSink.
func (a *EventAlertAdapter) Alert(ctx context.Context, handler, message string) {
	sendAlert(ctx, a.alerter, a.logger, out.Alert{
		Source:   handler,
		Severity: out.AlertWarning,
		Title:    fmt.Sprintf("Event handler %q needs attention", handler),
		Details:  message,
		DedupKey: "event:" + handler + ":" + reasonKey(message),
	})
}
