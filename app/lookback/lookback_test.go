package lookback

import (
	"fmt"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const wantErr = `invalid --since value %q: want a Go duration (24h), a day count (7d), or an RFC3339 date`
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"", time.Time{}, false},
		{"24h", now.Add(-24 * time.Hour), false},
		{"7d", now.Add(-168 * time.Hour), false},
		{"2026-11-01T00:00:00Z", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), false},
		{"0d", time.Time{}, true},
		{"-3d", time.Time{}, true},
		{"7x", time.Time{}, true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in, now)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("Parse(%q): want error", tt.in)
			}
			if exp := fmt.Sprintf(wantErr, tt.in); err.Error() != exp {
				t.Fatalf("Parse(%q) error = %q, want %q", tt.in, err.Error(), exp)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		if !got.Equal(tt.want) {
			t.Fatalf("Parse(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
