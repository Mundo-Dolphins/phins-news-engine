// Package telegramimport models a single message read from a Telegram
// channel's history — the minimal shape application/telegramimport needs
// to match a message back to a known local article and label it.
package telegramimport

import "time"

// Message is one message from a channel's history.
type Message struct {
	ID        int
	PostedAt  time.Time
	Text      string
	Views     int
	Forwards  int
	Reactions int
}
