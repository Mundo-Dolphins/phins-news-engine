// Package lookback parses the "since" lower bounds shared by the CLI
// commands and the admin actions.
package lookback

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse turns s into a lower time bound relative to now: a Go duration
// ("24h"), a bare day count ("7d", n > 0), an RFC3339 date, or "" (zero
// time, no lower bound).
func Parse(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since value %q: want a Go duration (24h), a day count (7d), or an RFC3339 date", s)
}
