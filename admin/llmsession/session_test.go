package llmsession_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/llmsession"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

func newSession() *llmsession.Session {
	servers := []config.LLMServerConfig{
		{Name: "server-a", BaseURL: "http://a/v1"},
		{Name: "server-b", BaseURL: "http://b/v1"},
	}
	list := func(_ context.Context, s config.LLMServerConfig) ([]string, error) {
		if s.Name == "server-b" {
			return nil, errors.New("boom")
		}
		return []string{"m1", "m2"}, nil
	}
	return llmsession.New(servers, list)
}

func TestSession(t *testing.T) {
	ctx := context.Background()
	s := newSession()
	if _, ok := s.Current(); ok {
		t.Fatal("new session has a selection")
	}
	if len(s.Servers()) != 2 {
		t.Fatalf("Servers = %v", s.Servers())
	}
	for _, sel := range []llmservers.Selection{{Model: "m1"}, {Server: "server-a"}, {}} {
		if err := s.Select(ctx, sel); err == nil {
			t.Errorf("Select(%+v) = nil, want error", sel)
		}
	}
	if err := s.Select(ctx, llmservers.Selection{Server: "server-a", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Select(ctx, llmservers.Selection{Server: "server-a", Model: "nope"}); err == nil {
		t.Fatal("unlisted model accepted")
	}
	if err := s.Select(ctx, llmservers.Selection{Server: "server-b", Model: "m1"}); err == nil {
		t.Fatal("unreachable server accepted")
	}
	if err := s.Select(ctx, llmservers.Selection{Server: "nope", Model: "m1"}); err == nil {
		t.Fatal("unknown server accepted")
	}
	got, ok := s.Current()
	if !ok || got != (llmservers.Selection{Server: "server-a", Model: "m1"}) {
		t.Fatalf("Current = %+v, %v after failed selects", got, ok)
	}
	models, err := s.Models(ctx, "server-a")
	if err != nil || len(models) != 2 {
		t.Fatalf("Models = %v, %v", models, err)
	}
	if _, err := s.Models(ctx, "nope"); err == nil {
		t.Fatal("Models of unknown server succeeded")
	}
	cfg, err := s.Apply(&config.Config{}, got)
	if err != nil || cfg == nil {
		t.Fatalf("Apply = %v, %v", cfg, err)
	}
	if _, err := s.Apply(&config.Config{}, llmservers.Selection{Server: "nope", Model: "m"}); err == nil {
		t.Fatal("Apply with unknown server succeeded")
	}
	s.Clear()
	if _, ok := s.Current(); ok {
		t.Fatal("Clear left a selection")
	}
}

func TestSessionConcurrent(t *testing.T) {
	s := newSession()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.Select(context.Background(), llmservers.Selection{Server: "server-a", Model: "m1"})
			s.Clear()
		}()
		go func() {
			defer wg.Done()
			s.Current()
		}()
	}
	wg.Wait()
}
