package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestLogEntityFallbackRedactsBaseURL(t *testing.T) {
	const secret = "SECRET-TOKEN-123"
	var cfg config.Config
	cfg.EntityExtraction.Fallback.BaseURL = "https://user:" + secret + "@llm.example.com/v1?key=" + secret
	cfg.EntityExtraction.Fallback.Model = "m"
	var rec recordingLogger
	logEntityFallback(&rec, &cfg)
	out := rec.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaks the credential:\n%s", out)
	}
	if !strings.Contains(out, "llm.example.com/v1") {
		t.Fatalf("log lost the host and path:\n%s", out)
	}
	if !strings.Contains(out, "no API key") {
		t.Fatalf("missing-key warning not logged:\n%s", out)
	}
}

type recordingLogger struct{ strings.Builder }

func (r *recordingLogger) Info(msg string, kv ...any) { fmt.Fprintln(r, "INFO", msg, kv) }
func (r *recordingLogger) Warn(msg string, kv ...any) { fmt.Fprintln(r, "WARN", msg, kv) }
