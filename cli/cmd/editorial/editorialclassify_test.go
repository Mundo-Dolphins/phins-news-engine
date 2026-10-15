// cli/cmd/editorial/editorialclassify_test.go
package editorial

import (
	"testing"
)

func TestResolveClassifySince(t *testing.T) {
	tests := []struct {
		name    string
		since   string
		all     bool
		wantErr bool
	}{
		{"empty means no bound", "", false, false},
		{"all overrides since", "24h", true, false},
		{"duration", "24h", false, false},
		{"day count", "7d", false, false},
		{"rfc3339", "2026-01-01T00:00:00Z", false, false},
		{"invalid", "not-a-time", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveClassifySince(tt.since, tt.all)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveClassifySince(%q, %v) error = %v, wantErr %v", tt.since, tt.all, err, tt.wantErr)
			}
			if tt.name == "empty means no bound" && !got.IsZero() {
				t.Errorf("got = %v, want zero time", got)
			}
			if tt.name == "all overrides since" && !got.IsZero() {
				t.Errorf("got = %v, want zero time when --all set", got)
			}
		})
	}
}

func TestNewClassifyCommand_HasExpectedFlags(t *testing.T) {
	cmd := newClassifyCommand()
	for _, name := range []string{"since", "all", "force", "batch-size", "limit", "llm-base-url", "llm-model", "llm-max-tokens", "llm-disable-thinking"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
	if bs, err := cmd.Flags().GetInt("batch-size"); err != nil || bs != 20 {
		t.Errorf("batch-size default = %d, err %v, want 20", bs, err)
	}
	if l, err := cmd.Flags().GetInt("limit"); err != nil || l != 500 {
		t.Errorf("limit default = %d, err %v, want 500", l, err)
	}
}
