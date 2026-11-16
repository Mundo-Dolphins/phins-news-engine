// Package llmsession holds the admin's in-memory LLM server selection. It is
// never persisted: a restart returns to the configured LLM settings.
package llmsession

import (
	"context"
	"errors"
	"sync"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmservers"
)

// Session is the admin's selectable LLM servers and current selection. It is
// safe for concurrent use.
type Session struct {
	mu      sync.Mutex
	servers []config.LLMServerConfig
	list    llmservers.ListFunc
	sel     *llmservers.Selection
}

// New returns a session over servers; list fetches a server's models.
func New(servers []config.LLMServerConfig, list llmservers.ListFunc) *Session {
	return &Session{servers: servers, list: list}
}

// Servers returns the configured servers.
func (s *Session) Servers() []config.LLMServerConfig { return s.servers }

// Current returns the selection and whether one is set.
func (s *Session) Current() (llmservers.Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sel == nil {
		return llmservers.Selection{}, false
	}
	return *s.sel, true
}

// Models lists the models the named server serves.
func (s *Session) Models(ctx context.Context, server string) ([]string, error) {
	srv, err := llmservers.Find(s.servers, server)
	if err != nil {
		return nil, err
	}
	return s.list(ctx, srv)
}

// Check verifies sel names a configured server that serves the model.
func (s *Session) Check(ctx context.Context, sel llmservers.Selection) error {
	if sel.Server == "" || sel.Model == "" {
		return errors.New("llm selection needs a server and a model")
	}
	srv, err := llmservers.Find(s.servers, sel.Server)
	if err != nil {
		return err
	}
	return llmservers.Check(ctx, s.list, srv, sel.Model)
}

// Select checks sel and, only if it passes, stores it. A failed check leaves
// the previous selection.
func (s *Session) Select(ctx context.Context, sel llmservers.Selection) error {
	if err := s.Check(ctx, sel); err != nil {
		return err
	}
	s.mu.Lock()
	s.sel = &sel
	s.mu.Unlock()
	return nil
}

// Clear drops the selection.
func (s *Session) Clear() {
	s.mu.Lock()
	s.sel = nil
	s.mu.Unlock()
}

// Apply returns cfg with sel's server and model applied (see llmservers.Apply).
func (s *Session) Apply(cfg *config.Config, sel llmservers.Selection) (*config.Config, error) {
	srv, err := llmservers.Find(s.servers, sel.Server)
	if err != nil {
		return nil, err
	}
	return llmservers.Apply(cfg, srv, sel.Model), nil
}
