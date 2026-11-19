package out

import (
	"context"
	"encoding/json"
)

// TelegramUpdate is one Bot API update reduced to what task routing needs:
// either a user message (optionally a reply) or an inline-button callback.
type TelegramUpdate struct {
	UpdateID       int64
	ChatID, UserID int64
	MessageID      int64
	// Text is the text of the user's message.
	Text string
	// ReplyToText is the text of the message being replied to, if any.
	ReplyToText  string
	CallbackID   string
	CallbackData string
}

// IsCallback reports whether the update is an inline-button press.
func (u TelegramUpdate) IsCallback() bool { return u.CallbackID != "" }

// TelegramUpdateRouter is implemented by the host: it decides which task an
// update belongs to and what signal payload to store for it.
type TelegramUpdateRouter interface {
	Route(u TelegramUpdate) (taskID int64, payload json.RawMessage, ok bool)
}

// TelegramOffsetRepository persists the next getUpdates offset per bot.
type TelegramOffsetRepository interface {
	// Load returns the stored offset, or 0 when there is none.
	Load(ctx context.Context, bot string) (int64, error)
	Store(ctx context.Context, bot string, next int64) error
}
