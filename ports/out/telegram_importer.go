// ports/out/telegram_importer.go
package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
)

// TelegramImportRequest describes one channel-history export request.
type TelegramImportRequest struct {
	ChannelRef   string
	Limit        int
	ResetSession bool
}

// TelegramChannelImporter reads a Telegram channel's message history. The
// library ships no implementation: it is an optional history client
// provided by the host (the Bot API cannot read a channel's own history).
type TelegramChannelImporter interface {
	ExportChannelMessages(ctx context.Context, req TelegramImportRequest) ([]telegramimport.Message, error)
}

// TelegramImportClientConfig configures the host-provided Telegram history client.
type TelegramImportClientConfig struct {
	APIID       int
	APIHash     string
	Phone       string
	SessionPath string
	// NonInteractive, when true, makes the resulting client refuse to read
	// from stdin during auth: Code/Password return an error immediately
	// instead of blocking. Defaults to false (interactive), which the CLI
	// callers rely on. Unattended long-running processes (the scheduler)
	// must set it true, because a stat-only "session file exists" check
	// cannot prove the session is still authorized.
	NonInteractive bool // refuse to read stdin during auth
}
