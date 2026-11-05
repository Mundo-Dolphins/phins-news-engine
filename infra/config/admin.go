package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

const (
	defaultAdminListenAddrs  = "127.0.0.1:8480"
	defaultAdminDBPath       = "data/admin.db"
	defaultAdminAllowedCIDRs = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"
	minAdminMCPTokenLen      = 32
)

// AdminConfig holds the admin panel settings. It is env-only (never read
// from YAML) and is checked by Validate only, so the bot and the CLI keep
// loading the same config without any ADMIN_* variable set.
type AdminConfig struct {
	// Username is the single panel login (ADMIN_USERNAME).
	Username string `yaml:"-"`
	// PasswordHash is the bcrypt hash of the panel password (ADMIN_PASSWORD_HASH).
	PasswordHash string `yaml:"-"`
	// MCPToken is the bearer token for the MCP endpoint (ADMIN_MCP_TOKEN).
	MCPToken string `yaml:"-"`
	// DBPath is the admin's own SQLite file (ADMIN_DB_PATH).
	DBPath string `yaml:"-"`
	// ListenAddrs are the host:port addresses to serve on (ADMIN_LISTEN_ADDRS).
	ListenAddrs []string `yaml:"-"`
	// AllowedCIDRs are the client networks admitted (ADMIN_ALLOWED_CIDRS).
	AllowedCIDRs []netip.Prefix `yaml:"-"`

	// cidrErr records the first unparsable ADMIN_ALLOWED_CIDRS entry so
	// Validate can report it (Load itself never fails on admin settings).
	cidrErr error
}

// applyAdminEnv fills a from the ADMIN_* environment variables, using the
// documented defaults for the list and path settings.
func applyAdminEnv(a *AdminConfig) {
	a.Username = os.Getenv("ADMIN_USERNAME")
	a.PasswordHash = os.Getenv("ADMIN_PASSWORD_HASH")
	a.MCPToken = os.Getenv("ADMIN_MCP_TOKEN")
	a.DBPath = envOrDefault("ADMIN_DB_PATH", defaultAdminDBPath)
	a.ListenAddrs = splitCSV(envOrDefault("ADMIN_LISTEN_ADDRS", defaultAdminListenAddrs))
	a.AllowedCIDRs, a.cidrErr = parseCIDRs(splitCSV(envOrDefault("ADMIN_ALLOWED_CIDRS", defaultAdminAllowedCIDRs)))
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// splitCSV splits a comma list, trimming blanks and dropping empty items.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseCIDRs parses every item, returning the valid prefixes and the first
// parse error (if any).
func parseCIDRs(items []string) ([]netip.Prefix, error) {
	var (
		out      []netip.Prefix
		firstErr error
	)
	for _, item := range items {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("ADMIN_ALLOWED_CIDRS: invalid CIDR %q: %w", item, err)
			}
			continue
		}
		out = append(out, p)
	}
	return out, firstErr
}

// Validate checks the settings the admin process cannot run without. It is
// called by the admin only; config.Load never calls it. Errors name the
// offending environment variable.
func (a AdminConfig) Validate() error {
	if a.Username == "" {
		return errors.New("ADMIN_USERNAME is not set")
	}
	if a.PasswordHash == "" {
		return errors.New("ADMIN_PASSWORD_HASH is not set")
	}
	if !strings.HasPrefix(a.PasswordHash, "$2") {
		return errors.New("ADMIN_PASSWORD_HASH must be a bcrypt hash (starting with $2)")
	}
	if a.MCPToken == "" {
		return errors.New("ADMIN_MCP_TOKEN is not set")
	}
	if len(a.MCPToken) < minAdminMCPTokenLen {
		return fmt.Errorf("ADMIN_MCP_TOKEN must be at least %d characters", minAdminMCPTokenLen)
	}
	if len(a.ListenAddrs) == 0 {
		return errors.New("ADMIN_LISTEN_ADDRS is empty")
	}
	for _, addr := range a.ListenAddrs {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("ADMIN_LISTEN_ADDRS: invalid address %q: %w", addr, err)
		}
	}
	if a.cidrErr != nil {
		return a.cidrErr
	}
	return nil
}
