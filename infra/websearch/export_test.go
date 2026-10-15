package websearch

import (
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ClientTimeoutForTest exposes the HTTP client timeout of a searcher
// built by New.
func ClientTimeoutForTest(s appout.WebSearcher) time.Duration {
	switch c := s.(type) {
	case *searxngClient:
		return c.httpClient.Timeout
	case *tavilyClient:
		return c.httpClient.Timeout
	}
	return 0
}
