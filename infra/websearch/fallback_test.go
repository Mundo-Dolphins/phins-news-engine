// infra/websearch/fallback_test.go
package websearch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type stubSearcher struct {
	res   []weblookup.SearchResult
	err   error
	calls int
}

func (s *stubSearcher) Search(context.Context, string, appout.SearchOptions) ([]weblookup.SearchResult, error) {
	s.calls++
	return s.res, s.err
}

func TestFallbackChain(t *testing.T) {
	hit := []weblookup.SearchResult{{URL: "https://league.example.com/x", Trusted: true}}
	t.Run("primary success never touches the secondary", func(t *testing.T) {
		p, s := &stubSearcher{res: hit}, &stubSearcher{}
		got, err := websearch.NewFallbackChain(p, s, 10, nil).Search(context.Background(), "q", appout.SearchOptions{})
		if err != nil || len(got) != 1 || s.calls != 0 {
			t.Fatalf("got %v, %v, secondary calls %d", got, err, s.calls)
		}
	})
	t.Run("outage on the primary uses the secondary", func(t *testing.T) {
		p, s := &stubSearcher{err: appout.ErrSearchProviderUnavailable}, &stubSearcher{res: hit}
		notified := 0
		got, err := websearch.NewFallbackChain(p, s, 10, func(error) { notified++ }).Search(context.Background(), "q", appout.SearchOptions{})
		if err != nil || len(got) != 1 || s.calls != 1 || notified != 1 {
			t.Fatalf("got %v, %v, secondary calls %d, notified %d", got, err, s.calls, notified)
		}
	})
	t.Run("a non-outage primary error is not retried on the secondary", func(t *testing.T) {
		p, s := &stubSearcher{err: errors.New("decode error")}, &stubSearcher{res: hit}
		if _, err := websearch.NewFallbackChain(p, s, 10, nil).Search(context.Background(), "q", appout.SearchOptions{}); err == nil || s.calls != 0 {
			t.Fatalf("err = %v, secondary calls %d; want the primary error and no secondary call", err, s.calls)
		}
	})
	t.Run("secondary budget is enforced and reported as an outage", func(t *testing.T) {
		p, s := &stubSearcher{err: appout.ErrSearchProviderUnavailable}, &stubSearcher{res: hit}
		chain := websearch.NewFallbackChain(p, s, 2, nil)
		for i := 0; i < 2; i++ {
			if _, err := chain.Search(context.Background(), "q", appout.SearchOptions{}); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		_, err := chain.Search(context.Background(), "q", appout.SearchOptions{})
		if !errors.Is(err, appout.ErrSearchProviderUnavailable) || s.calls != 2 {
			t.Fatalf("err = %v, secondary calls %d; want outage error and exactly 2 secondary calls", err, s.calls)
		}
	})
	t.Run("both providers down", func(t *testing.T) {
		p := &stubSearcher{err: appout.ErrSearchProviderUnavailable}
		s := &stubSearcher{err: appout.ErrSearchProviderUnavailable}
		_, err := websearch.NewFallbackChain(p, s, 10, nil).Search(context.Background(), "q", appout.SearchOptions{})
		if !errors.Is(err, appout.ErrSearchProviderUnavailable) {
			t.Fatalf("err = %v, want outage error so the circuit breaker can trip", err)
		}
	})
}
