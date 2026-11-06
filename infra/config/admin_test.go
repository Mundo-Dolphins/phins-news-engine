package config_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

var adminEnvVars = []string{
	"ADMIN_USERNAME", "ADMIN_PASSWORD_HASH", "ADMIN_MCP_TOKEN",
	"ADMIN_LISTEN_ADDRS", "ADMIN_DB_PATH", "ADMIN_ALLOWED_CIDRS",
}

func clearAdminEnv(t *testing.T) {
	t.Helper()
	for _, k := range adminEnvVars {
		t.Setenv(k, "")
	}
}

func TestAdminEnvDefaults(t *testing.T) {
	clearAdminEnv(t)
	cfg, err := config.Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatalf("Load() must not require ADMIN_* vars: %v", err)
	}
	a := cfg.Admin
	if a.Username != "" || a.PasswordHash != "" || a.MCPToken != "" {
		t.Errorf("credentials should default empty, got %+v", a)
	}
	if got := strings.Join(a.ListenAddrs, ","); got != "127.0.0.1:8480" {
		t.Errorf("ListenAddrs = %q", got)
	}
	if a.DBPath != "data/admin.db" {
		t.Errorf("DBPath = %q", a.DBPath)
	}
	var got []string
	for _, p := range a.AllowedCIDRs {
		got = append(got, p.String())
	}
	want := "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"
	if strings.Join(got, ",") != want {
		t.Errorf("AllowedCIDRs = %v, want %s", got, want)
	}
}

func TestAdminEnvOverrides(t *testing.T) {
	clearAdminEnv(t)
	t.Setenv("ADMIN_USERNAME", "root")
	t.Setenv("ADMIN_PASSWORD_HASH", "$2a$10$abc")
	t.Setenv("ADMIN_MCP_TOKEN", strings.Repeat("t", 32))
	t.Setenv("ADMIN_LISTEN_ADDRS", " 127.0.0.1:9000 , [::1]:9000 ")
	t.Setenv("ADMIN_DB_PATH", "var/admin.db")
	t.Setenv("ADMIN_ALLOWED_CIDRS", " 10.1.0.0/16 , ::1/128 ")
	cfg, err := config.Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	a := cfg.Admin
	if a.Username != "root" || a.PasswordHash != "$2a$10$abc" || a.MCPToken != strings.Repeat("t", 32) || a.DBPath != "var/admin.db" {
		t.Errorf("scalar overrides not applied: %+v", a)
	}
	if strings.Join(a.ListenAddrs, "|") != "127.0.0.1:9000|[::1]:9000" {
		t.Errorf("ListenAddrs = %q", a.ListenAddrs)
	}
	wantCIDRs := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("::1/128")}
	if len(a.AllowedCIDRs) != 2 || a.AllowedCIDRs[0] != wantCIDRs[0] || a.AllowedCIDRs[1] != wantCIDRs[1] {
		t.Errorf("AllowedCIDRs = %v", a.AllowedCIDRs)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestAdminEnvCredentialsTrimmed(t *testing.T) {
	clearAdminEnv(t)
	t.Setenv("ADMIN_USERNAME", "  root\n")
	t.Setenv("ADMIN_PASSWORD_HASH", " $2a$12$abc\n")
	t.Setenv("ADMIN_MCP_TOKEN", "\t"+strings.Repeat("t", 32)+" ")
	cfg, err := config.Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Admin
	if a.Username != "root" || a.PasswordHash != "$2a$12$abc" || a.MCPToken != strings.Repeat("t", 32) {
		t.Errorf("credentials not trimmed: %q %q %q", a.Username, a.PasswordHash, a.MCPToken)
	}
}

func TestAdminValidate(t *testing.T) {
	good := map[string]string{
		"ADMIN_USERNAME":      "root",
		"ADMIN_PASSWORD_HASH": "$2a$10$abc",
		"ADMIN_MCP_TOKEN":     strings.Repeat("t", 32),
	}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"valid", nil, ""},
		{"missing user", map[string]string{"ADMIN_USERNAME": ""}, "ADMIN_USERNAME is not set"},
		{"missing hash", map[string]string{"ADMIN_PASSWORD_HASH": ""}, "ADMIN_PASSWORD_HASH is not set"},
		{"bad hash prefix", map[string]string{"ADMIN_PASSWORD_HASH": "plaintext"}, "ADMIN_PASSWORD_HASH"},
		{"missing token", map[string]string{"ADMIN_MCP_TOKEN": ""}, "ADMIN_MCP_TOKEN is not set"},
		{"short token", map[string]string{"ADMIN_MCP_TOKEN": "short"}, "ADMIN_MCP_TOKEN"},
		{"bad cidr", map[string]string{"ADMIN_ALLOWED_CIDRS": "10.0.0.0/8,nope"}, "ADMIN_ALLOWED_CIDRS"},
		{"bad addr", map[string]string{"ADMIN_LISTEN_ADDRS": "127.0.0.1"}, "ADMIN_LISTEN_ADDRS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAdminEnv(t)
			for k, v := range good {
				t.Setenv(k, v)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load(writeFixture(t, validYAML))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			err = cfg.Admin.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
