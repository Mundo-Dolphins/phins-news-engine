package llmservers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

func TestFind(t *testing.T) {
	servers := []config.LLMServerConfig{{Name: "server-a"}, {Name: "server-b", BaseURL: "http://b/v1"}}
	got, err := llmservers.Find(servers, "server-b")
	if err != nil || got.BaseURL != "http://b/v1" {
		t.Fatalf("Find = %+v, %v", got, err)
	}
	_, err = llmservers.Find(servers, "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{`unknown llm server "nope"`, "server-a", "server-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	_, err = llmservers.Find(nil, "x")
	if err == nil || !strings.Contains(err.Error(), "no llm_servers configured") {
		t.Fatalf("empty list error = %v", err)
	}
}

func TestListModels(t *testing.T) {
	t.Run("sorted, both base url forms, auth header", func(t *testing.T) {
		var gotAuth string
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":[{"id":"b-model"},{"id":"a-model"}]}`))
		}))
		defer srv.Close()
		t.Setenv("LLMSERVERS_TEST_KEY", "k1")
		for _, base := range []string{srv.URL + "/v1", srv.URL + "/v1/"} {
			got, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{Name: "server-a", BaseURL: base, APIKeyEnv: "LLMSERVERS_TEST_KEY"})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0] != "a-model" || got[1] != "b-model" {
				t.Fatalf("got %v", got)
			}
			if gotPath != "/v1/models" {
				t.Errorf("path = %q", gotPath)
			}
			if gotAuth != "Bearer k1" {
				t.Errorf("auth = %q", gotAuth)
			}
		}
		if _, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{Name: "server-a", BaseURL: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "" {
			t.Errorf("auth without env = %q", gotAuth)
		}
	})
	t.Run("html body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
		defer srv.Close()
		if _, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{BaseURL: srv.URL + "/v1"}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
		defer srv.Close()
		_, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{BaseURL: srv.URL + "/v1"})
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("closed server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		if _, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{BaseURL: url + "/v1"}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty data", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) }))
		defer srv.Close()
		got, err := llmservers.ListModels(context.Background(), config.LLMServerConfig{BaseURL: srv.URL + "/v1"})
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("got %#v, %v", got, err)
		}
	})
}

func TestCheck(t *testing.T) {
	s := config.LLMServerConfig{Name: "server-a"}
	list := func(context.Context, config.LLMServerConfig) ([]string, error) { return []string{"m1"}, nil }
	if err := llmservers.Check(context.Background(), list, s, "m1"); err != nil {
		t.Fatal(err)
	}
	err := llmservers.Check(context.Background(), list, s, "m2")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{`model "m2" is not available on server-a`, "llm models --llm-server server-a"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	bad := func(context.Context, config.LLMServerConfig) ([]string, error) { return nil, errors.New("boom") }
	err = llmservers.Check(context.Background(), bad, s, "m1")
	if err == nil || !strings.Contains(err.Error(), "llm server server-a unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestApply(t *testing.T) {
	cfg := &config.Config{}
	cfg.OfflineTraining.LLM = config.OfflineTrainingLLMConfig{BaseURL: "http://yaml/v1", Model: "y", APIKey: "old", Temperature: 0.3, MaxTokens: 99, TimeoutSeconds: 7}
	cfg.EntityDiscovery.LLM = config.EntityDiscoveryLLMConfig{BaseURL: "http://yaml/v1", Model: "y", APIKey: "old", Temperature: 0.3, MaxTokens: 99, TimeoutSeconds: 7}
	cfg.EntityDiscovery.Enabled = true
	s := config.LLMServerConfig{Name: "server-a", BaseURL: "http://a/v1", APIKeyEnv: "LLMSERVERS_TEST_APPLY_KEY"}

	check := func(got *config.Config, key string) {
		t.Helper()
		o, e := got.OfflineTraining.LLM, got.EntityDiscovery.LLM
		if o.BaseURL != "http://a/v1" || o.Model != "m1" || o.APIKey != key {
			t.Errorf("offline = %+v", o)
		}
		if e.BaseURL != "http://a/v1" || e.Model != "m1" || e.APIKey != key {
			t.Errorf("discovery = %+v", e)
		}
		if o.Temperature != 0.3 || o.MaxTokens != 99 || o.TimeoutSeconds != 7 || e.Temperature != 0.3 || e.MaxTokens != 99 || e.TimeoutSeconds != 7 {
			t.Errorf("other settings changed: %+v %+v", o, e)
		}
		if !got.EntityDiscovery.Enabled {
			t.Error("Enabled changed")
		}
	}
	os.Unsetenv("LLMSERVERS_TEST_APPLY_KEY")
	check(llmservers.Apply(cfg, s, "m1"), "")
	t.Setenv("LLMSERVERS_TEST_APPLY_KEY", "secret")
	check(llmservers.Apply(cfg, s, "m1"), "secret")

	if cfg.OfflineTraining.LLM.BaseURL != "http://yaml/v1" || cfg.OfflineTraining.LLM.APIKey != "old" || cfg.EntityDiscovery.LLM.Model != "y" {
		t.Errorf("input mutated: %+v %+v", cfg.OfflineTraining.LLM, cfg.EntityDiscovery.LLM)
	}
}
