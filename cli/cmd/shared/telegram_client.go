package shared

import (
	"errors"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ErrNoTelegramImportClient is returned by the default NewTelegramImportClient
// when no real factory has been registered.
var ErrNoTelegramImportClient = errors.New("no Telegram history client registered")

// NewTelegramImportClient constructs the out.TelegramChannelImporter used
// by both `telegram import` and `editorial cycle`'s Telegram-history step.
// A package-level var (not a local call) — like NewRemoteSyncClient — so
// tests can swap in a fake without a live MTProto login, and main can
// register the real gotd-backed factory via SetTelegramImportClient.
var NewTelegramImportClient = func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error) {
	return nil, ErrNoTelegramImportClient
}

// SetTelegramImportClient registers the factory used to build the Telegram
// history client.
func SetTelegramImportClient(f func(out.TelegramImportClientConfig) (out.TelegramChannelImporter, error)) {
	NewTelegramImportClient = f
}

// ValidateTelegramImportConfig checks every prerequisite a Telegram
// history import needs before it touches anything (see
// config.TelegramConfig.ValidateImport).
func ValidateTelegramImportConfig(cfg config.TelegramConfig) error {
	return cfg.ValidateImport()
}
