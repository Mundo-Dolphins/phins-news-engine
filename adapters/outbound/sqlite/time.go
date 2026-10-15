package sqlite

import (
	"fmt"
	"strings"
	"time"
)

const sqliteTimeFormat = time.RFC3339Nano

type sqliteNullTime struct {
	Time  time.Time
	Valid bool
}

func (t *sqliteNullTime) Scan(value any) error {
	parsed, valid, err := parseSQLiteTime(value)
	if err != nil {
		return err
	}
	t.Time = parsed
	t.Valid = valid
	return nil
}

func sqliteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return sqliteTime(t)
}

func parseSQLiteTime(value any) (time.Time, bool, error) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, false, nil
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false, nil
		}
		return v.UTC(), true, nil
	case string:
		return parseSQLiteTimeString(v)
	case []byte:
		return parseSQLiteTimeString(string(v))
	default:
		return time.Time{}, false, fmt.Errorf("sqlite: unsupported timestamp type %T", value)
	}
}

func parseSQLiteTimeString(value string) (time.Time, bool, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return time.Time{}, false, nil
	}
	if idx := strings.Index(s, " m=+"); idx >= 0 {
		s = s[:idx]
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 -0700",
		"2006-01-02 15:04:05 -0700 -0700",
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("sqlite: parse timestamp %q", value)
}
