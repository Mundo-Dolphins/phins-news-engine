package shared

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
)

func TestSetFetcherExtensionsStoresValue(t *testing.T) {
	old := FetcherExtensions
	t.Cleanup(func() { FetcherExtensions = old })

	SetFetcherExtensions(fetcherwiring.Extensions{Categories: map[string]fetcherwiring.CategoryBuilder{"website": nil}})
	if _, ok := FetcherExtensions.Categories["website"]; !ok {
		t.Fatal("FetcherExtensions not stored")
	}
}
