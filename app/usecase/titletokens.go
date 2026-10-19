package usecase

import (
	"sync"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/storyguard"
)

// titleTokenCache memoizes the dedup tokens of article titles. The story
// guard and the lexical cluster join compare every incoming article with up
// to a couple of thousand recent titles per cycle; without the cache each of
// those comparisons would normalize the same titles again. A title never
// changes for a given article ID, so entries never go stale; the cache is
// simply emptied when it grows past maxEntries.
type titleTokenCache struct {
	mu sync.Mutex
	m  map[string][]string
}

const titleTokenCacheMaxEntries = 20000

func (c *titleTokenCache) get(articleID, title string, cfg normalization.TextConfig) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tokens, ok := c.m[articleID]; ok {
		return tokens
	}
	if c.m == nil || len(c.m) >= titleTokenCacheMaxEntries {
		c.m = make(map[string][]string, 4096)
	}
	tokens := storyguard.Tokenize(title, cfg)
	c.m[articleID] = tokens
	return tokens
}
