package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type noRouter struct{}

func (noRouter) Route(out.TelegramUpdate) (int64, json.RawMessage, bool) { return 0, nil, false }

func TestTelegramUpdatesValidate(t *testing.T) {
	const tok = "123456:secret"
	cases := []struct {
		name    string
		tu      TelegramUpdates
		token   string
		wantErr string
	}{
		{"ok", TelegramUpdates{AllowedUserID: 1, AllowedChatID: 2, Router: noRouter{}}, tok, ""},
		{"zero user", TelegramUpdates{AllowedChatID: 2, Router: noRouter{}}, tok, "AllowedUserID"},
		{"zero chat", TelegramUpdates{AllowedUserID: 1, Router: noRouter{}}, tok, "AllowedChatID"},
		{"no router", TelegramUpdates{AllowedUserID: 1, AllowedChatID: 2}, tok, "Router"},
		{"no token", TelegramUpdates{AllowedUserID: 1, AllowedChatID: 2, Router: noRouter{}}, "", "TELEGRAM_BOT_TOKEN"},
	}
	for _, c := range cases {
		err := c.tu.validate(c.token)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
	if botKey("123456:secret") != "123456" || botKey("nocolon") != "" {
		t.Errorf("botKey wrong")
	}
}
