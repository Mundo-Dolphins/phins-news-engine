package bluesky_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/bluesky"
)

func TestCountGraphemes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"precomposed accents", "canción", 7},
		{"combining accent", "canción", 7},
		{"enye combining", "ñ", 1},
		{"simple emoji", "🏈", 1},
		{"emoji with variation selector", "❤️", 1},
		{"skin tone", "👍🏽", 1},
		{"zwj family", "👨‍👩‍👧‍👦", 1},
		{"zwj profession", "👩🏽‍🚒", 1},
		{"flag", "🇪🇸", 1},
		{"two flags", "🇪🇸🇺🇸", 2},
		{"three regional indicators", "🇪🇸🇺", 2},
		{"keycap", "1️⃣", 1},
		{"tag sequence flag", "🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", 1},
		{"crlf", "a\r\nb", 3},
		{"mixed", "Go 🏈 año 🇪🇸!", 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bluesky.CountGraphemesForTest(tt.in); got != tt.want {
				t.Errorf("countGraphemes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
