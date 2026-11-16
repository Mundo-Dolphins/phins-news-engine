package llm_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

const twoServers = `
llm_servers:
  - name: server-a
    base_url: http://server-a:8000/v1
  - name: server-b
    base_url: http://server-b:11434/v1
`

func setup(t *testing.T, extra string, a, b error) string {
	t.Helper()
	path := testsupport.WriteFixture(t)
	if extra != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(extra); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	orig := shared.ListLLMModels
	t.Cleanup(func() { shared.ListLLMModels = orig })
	shared.ListLLMModels = func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		switch s.Name {
		case "server-a":
			if a != nil {
				return nil, a
			}
			return []string{"m1", "m2"}, nil
		default:
			if b != nil {
				return nil, b
			}
			return []string{"m3"}, nil
		}
	}
	return path
}

func TestModels_ListsAllServersInConfigOrder(t *testing.T) {
	path := setup(t, twoServers, nil, errors.New("connection refused"))
	got, err := testsupport.Execute(t, "--config", path, "llm", "models")
	if err != nil {
		t.Fatalf("error = %v, output: %s", err, got)
	}
	want := "server-a  http://server-a:8000/v1\n  m1\n  m2\nserver-b  http://server-b:11434/v1\n  unavailable: connection refused\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestModels_AllFailingIsError(t *testing.T) {
	path := setup(t, twoServers, errors.New("boom"), errors.New("connection refused"))
	if _, err := testsupport.Execute(t, "--config", path, "llm", "models"); err == nil {
		t.Fatal("expected error when every server fails")
	}
}

func TestModels_SingleServer(t *testing.T) {
	path := setup(t, twoServers, nil, errors.New("connection refused"))
	got, err := testsupport.Execute(t, "--config", path, "llm", "models", "--llm-server", "server-a")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if want := "server-a  http://server-a:8000/v1\n  m1\n  m2\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, err := testsupport.Execute(t, "--config", path, "llm", "models", "--llm-server", "server-b"); err == nil {
		t.Fatal("expected error for failing single server")
	}
	if _, err := testsupport.Execute(t, "--config", path, "llm", "models", "--llm-server", "nope"); err == nil || !strings.Contains(err.Error(), "unknown llm server") {
		t.Fatalf("error = %v, want unknown llm server", err)
	}
}

func TestModels_NoServersConfigured(t *testing.T) {
	path := setup(t, "", nil, nil)
	_, err := testsupport.Execute(t, "--config", path, "llm", "models")
	if err == nil || !strings.Contains(err.Error(), "no llm_servers configured") {
		t.Fatalf("error = %v, want no llm_servers configured", err)
	}
}

func TestModels_ServerWithNoModelsSaysSo(t *testing.T) {
	path := setup(t, twoServers, nil, nil)
	shared.ListLLMModels = func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		if s.Name == "server-a" {
			return nil, nil
		}
		return []string{"m3"}, nil
	}
	got, err := testsupport.Execute(t, "--config", path, "llm", "models")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	want := "server-a  http://server-a:8000/v1\n  (no models)\nserver-b  http://server-b:11434/v1\n  m3\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
