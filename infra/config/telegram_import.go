package config

import (
	"fmt"
	"os"
	"strings"
)

// ValidateImport checks every prerequisite a Telegram history import needs
// before it touches anything: api_id, api_hash, channel_id, and either a
// phone number or an already-reusable session file. It performs no I/O
// beyond that reusable-session stat and never includes a credential's value
// in its error — only which field is missing, by name.
func (c TelegramConfig) ValidateImport() error {
	var missing []string
	if c.APIID <= 0 {
		missing = append(missing, "telegram.api_id (TELEGRAM_API_ID)")
	}
	if strings.TrimSpace(c.APIHash) == "" {
		missing = append(missing, "telegram.api_hash (TELEGRAM_API_HASH)")
	}
	if strings.TrimSpace(c.ChannelID) == "" {
		missing = append(missing, "telegram.channel_id")
	}
	if strings.TrimSpace(c.Phone) == "" && !hasReusableSession(c.SessionPath) {
		missing = append(missing, "telegram.phone (TELEGRAM_PHONE) or an existing session file at telegram.session_path")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required telegram import configuration: %s", strings.Join(missing, "; "))
	}
	return nil
}

// hasReusableSession reports whether path names an existing, non-empty
// session file (same rule as the host Telegram adapter's reusable-session check).
func hasReusableSession(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}
