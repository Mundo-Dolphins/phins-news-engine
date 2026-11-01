package publishing

import "fmt"

// Destination names where one outbox row must be delivered. Every row has
// exactly one; an article may have one row per destination (ADR 0031).
// The zero value is Telegram so legacy callers and rows keep their meaning.
type Destination string

const (
	DestinationTelegram Destination = "telegram"
	DestinationBluesky  Destination = "bluesky"
)

// KnownDestinations lists every destination the schema accepts. Keep it in
// sync with the CHECK constraint in migrations/0046.
func KnownDestinations() []Destination {
	return []Destination{DestinationTelegram, DestinationBluesky}
}

// Valid reports whether d is empty (Telegram) or a known destination.
func (d Destination) Valid() bool {
	if d == "" {
		return true
	}
	for _, k := range KnownDestinations() {
		if d == k {
			return true
		}
	}
	return false
}

// Normalized maps the zero value to Telegram.
func (d Destination) Normalized() Destination {
	if d == "" {
		return DestinationTelegram
	}
	return d
}

// ValidateDestination returns an error for an unknown destination.
func ValidateDestination(d Destination) error {
	if !d.Valid() {
		return fmt.Errorf("publication destination must be one of %v, got %q", KnownDestinations(), d)
	}
	return nil
}
