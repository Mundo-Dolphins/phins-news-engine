// infra/websearch/noop_test.go
package websearch_test

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestNoop_SearchAlwaysErrors(t *testing.T) {
	s := websearch.NewNoop()
	results, err := s.Search(context.Background(), "\"Jai'Rene' Mossbank\" Isotopes", appout.SearchOptions{MaxResults: 5})
	if err == nil {
		t.Fatal("Search() error = nil, want an error (no provider configured in V1)")
	}
	if results != nil {
		t.Fatalf("Search() results = %+v, want nil", results)
	}
}
