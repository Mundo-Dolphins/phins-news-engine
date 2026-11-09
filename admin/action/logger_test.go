package action

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestPortLoggerError(t *testing.T) {
	var buf bytes.Buffer
	l := PortLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	l.Error("failed", errors.New("boom"), "id", 7)
	out := buf.String()
	i, j := strings.Index(out, "error=boom"), strings.Index(out, "id=7")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("want error=boom before id=7, got %q", out)
	}
}
