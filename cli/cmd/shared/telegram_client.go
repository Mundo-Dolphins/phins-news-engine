package shared

import (
	"errors"
	"fmt"
	"os"
	"strings"

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

// hasReusableSession reports whether path names an existing, non-empty
// session file (same rule as the gotd adapter's HasReusableSession).
func hasReusableSession(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// validateTelegramImportConfig checks every prerequisite a Telegram
// history import needs before it touches anything: api_id, api_hash,
// channel_id, and either a phone number or an already-reusable session
// file. It performs no I/O beyond that reusable-session stat and never
// includes a credential's value in its error — only which field is
// missing, by name.
func ValidateTelegramImportConfig(cfg config.TelegramConfig) error {
	var missing []string
	if cfg.APIID <= 0 {
		missing = append(missing, "telegram.api_id (TELEGRAM_API_ID)")
	}
	if strings.TrimSpace(cfg.APIHash) == "" {
		missing = append(missing, "telegram.api_hash (TELEGRAM_API_HASH)")
	}
	if strings.TrimSpace(cfg.ChannelID) == "" {
		missing = append(missing, "telegram.channel_id")
	}
	if strings.TrimSpace(cfg.Phone) == "" && !hasReusableSession(cfg.SessionPath) {
		missing = append(missing, "telegram.phone (TELEGRAM_PHONE) or an existing session file at telegram.session_path")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required telegram import configuration: %s", strings.Join(missing, "; "))
	}
	return nil
}
