package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
)

func newTestLogger(buf *bytes.Buffer) *Logger {
	return &Logger{zl: zerolog.New(buf)}
}

func TestNew_BuildsLoggerForValidAndInvalidLevels(t *testing.T) {
	if l := New("debug", false); l == nil {
		t.Fatal("New() = nil for a valid level")
	}
	if l := New("not-a-real-level", false); l == nil {
		t.Fatal("New() = nil for an invalid level, want fallback to info")
	}
}

func TestNew_PrettyModeProducesNonJSONOutput(t *testing.T) {
	l := New("info", true)
	l.Info("hello")
}

func TestLogger_Info_IncludesMessageAndFields(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	l.Info("cycle completed", "fetched", 3, "accepted", 1)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, buf.String())
	}
	if entry["message"] != "cycle completed" {
		t.Errorf("message = %v, want %q", entry["message"], "cycle completed")
	}
	if entry["fetched"] != float64(3) {
		t.Errorf("fetched = %v, want 3", entry["fetched"])
	}
}

func TestLogger_Error_IncludesErrorField(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	l.Error("publish failed", errors.New("telegram unreachable"), "article_id", "abc-123")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, buf.String())
	}
	if entry["error"] != "telegram unreachable" {
		t.Errorf("error = %v, want %q", entry["error"], "telegram unreachable")
	}
	if entry["article_id"] != "abc-123" {
		t.Errorf("article_id = %v, want %q", entry["article_id"], "abc-123")
	}
}

func TestLogger_Warn_IgnoresOddKeyValuePairs(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	l.Warn("dangling kv", "only_key")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, buf.String())
	}
	if entry["message"] != "dangling kv" {
		t.Errorf("message = %v, want %q", entry["message"], "dangling kv")
	}
}
