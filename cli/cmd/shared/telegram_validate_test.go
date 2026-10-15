package shared

import (
	"os"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestValidateTelegramImportConfig_AllPresent_NoError(t *testing.T) {
	cfg := config.TelegramConfig{APIID: 12345, APIHash: "hash", ChannelID: "-1001234567890", Phone: "+15551234567"}
	if err := ValidateTelegramImportConfig(cfg); err != nil {
		t.Errorf("ValidateTelegramImportConfig() error = %v, want nil", err)
	}
}

func TestValidateTelegramImportConfig_ReusableSessionSatisfiesPhoneRequirement(t *testing.T) {
	dir := t.TempDir()
	sessionPath := dir + "/session.json"
	if err := os.WriteFile(sessionPath, []byte("session-bytes"), 0o600); err != nil {
		t.Fatalf("write session fixture: %v", err)
	}
	cfg := config.TelegramConfig{APIID: 12345, APIHash: "hash", ChannelID: "-1001234567890", SessionPath: sessionPath}
	if err := ValidateTelegramImportConfig(cfg); err != nil {
		t.Errorf("ValidateTelegramImportConfig() error = %v, want nil (an existing session file substitutes for a phone number)", err)
	}
}

func TestValidateTelegramImportConfig_ReportsEveryMissingField(t *testing.T) {
	err := ValidateTelegramImportConfig(config.TelegramConfig{})
	if err == nil {
		t.Fatal("ValidateTelegramImportConfig() error = nil, want an error listing every missing field")
	}
	for _, want := range []string{"api_id", "api_hash", "channel_id", "phone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// TestValidateTelegramImportConfig_NeverIncludesAPIHashValue sets a real
// APIHash value alongside a different missing field (channel_id), so the
// returned error actually has an opportunity to leak it — then asserts it
// doesn't. A case where APIHash itself is the missing field can't test
// this: there is no value to leak.
func TestValidateTelegramImportConfig_NeverIncludesAPIHashValue(t *testing.T) {
	const secretHash = "super-secret-api-hash-value"
	err := ValidateTelegramImportConfig(config.TelegramConfig{APIID: 1, Phone: "y", APIHash: secretHash})
	if err == nil {
		t.Fatal("ValidateTelegramImportConfig() error = nil, want an error for missing channel_id")
	}
	if strings.Contains(err.Error(), secretHash) {
		t.Errorf("error = %q, must never contain the configured api_hash value", err.Error())
	}
}
