// app/editorialtrain/dedup.go
package editorialtrain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var dedupWhitespace = regexp.MustCompile(`\s+`)

// normalizeForDedup lowercases and collapses whitespace — simpler than
// domain/editorial.tokenize on purpose: dedup only needs to compare whole
// documents for near-identical wording, not build a feature vocabulary.
func normalizeForDedup(s string) string {
	return strings.TrimSpace(dedupWhitespace.ReplaceAllString(strings.ToLower(s), " "))
}

// exactDupKey returns a stable key for ex's normalized title+body — two
// examples with the same key are the same content modulo case/whitespace.
func exactDupKey(ex TrainingExample) string {
	sum := sha256.Sum256([]byte(normalizeForDedup(ex.Title) + "\x00" + normalizeForDedup(ex.Body)))
	return hex.EncodeToString(sum[:])
}

// shingles returns the set of 3-word shingles of s's normalized text (the
// whole normalized text as a single "shingle" if it has fewer than 3
// words, so very short titles still compare meaningfully instead of
// contributing an empty set).
func shingles(s string) map[string]bool {
	words := strings.Fields(normalizeForDedup(s))
	set := map[string]bool{}
	const n = 3
	if len(words) < n {
		if len(words) > 0 {
			set[strings.Join(words, " ")] = true
		}
		return set
	}
	for i := 0; i+n <= len(words); i++ {
		set[strings.Join(words[i:i+n], " ")] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// unionFind is a minimal, deterministic union-find over string IDs: the
// root of a group is always its lexicographically smallest member,
// regardless of union order — this determinism is what lets tests assert
// exact group membership without depending on iteration order.
type unionFind struct{ parent map[string]string }

func newUnionFind(ids []string) *unionFind {
	parent := make(map[string]string, len(ids))
	for _, id := range ids {
		parent[id] = id
	}
	return &unionFind{parent: parent}
}

func (u *unionFind) find(id string) string {
	if u.parent[id] != id {
		u.parent[id] = u.find(u.parent[id])
	}
	return u.parent[id]
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
}

// leakageGroups computes, for every example, the ID of the earliest
// (lexicographically smallest ArticleID) member of the group it belongs
// to once three signals are unioned together: sharing a non-empty
// ClusterID, sharing an exact-duplicate key, and (only when
// nearDupThreshold > 0) clustering together at >= nearDupThreshold
// title+body shingle Jaccard similarity. Split uses this instead of
// grouping by ClusterID alone, so none of the three ways an "event"
// repeats can leak across train/validate/test. O(n^2) near-dup
// comparison — acceptable at editorial-review-throughput scale (see the
// design doc for the same scale argument #155 already made for
// editorial_examples).
func leakageGroups(examples []TrainingExample, nearDupThreshold float64) map[string]string {
	ids := make([]string, len(examples))
	for i, ex := range examples {
		ids[i] = ex.ArticleID
	}
	uf := newUnionFind(ids)

	byCluster := map[string][]string{}
	byExactDup := map[string][]string{}
	for _, e := range examples {
		if e.ClusterID != "" {
			byCluster[e.ClusterID] = append(byCluster[e.ClusterID], e.ArticleID)
		}
		key := exactDupKey(e)
		byExactDup[key] = append(byExactDup[key], e.ArticleID)
	}
	for _, group := range byCluster {
		for i := 1; i < len(group); i++ {
			uf.union(group[0], group[i])
		}
	}
	for _, group := range byExactDup {
		for i := 1; i < len(group); i++ {
			uf.union(group[0], group[i])
		}
	}

	if nearDupThreshold > 0 {
		sets := make([]map[string]bool, len(examples))
		for i, e := range examples {
			sets[i] = shingles(e.Title + " " + e.Body)
		}
		for i := 0; i < len(examples); i++ {
			if len(sets[i]) == 0 {
				continue
			}
			for j := i + 1; j < len(examples); j++ {
				if len(sets[j]) == 0 {
					continue
				}
				if jaccard(sets[i], sets[j]) >= nearDupThreshold {
					uf.union(examples[i].ArticleID, examples[j].ArticleID)
				}
			}
		}
	}

	result := make(map[string]string, len(examples))
	for _, id := range ids {
		result[id] = uf.find(id)
	}
	return result
}
