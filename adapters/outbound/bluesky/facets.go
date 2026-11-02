package bluesky

import (
	"regexp"
	"strings"
)

// Facet types of the app.bsky.richtext.facet lexicon.
const (
	facetTypeLink = "app.bsky.richtext.facet#link"
	facetTypeTag  = "app.bsky.richtext.facet#tag"
)

// postFacet is one rich-text annotation. Offsets are UTF-8 byte offsets into
// the post text, as the protocol requires.
type postFacet struct {
	Index    facetIndex     `json:"index"`
	Features []facetFeature `json:"features"`
}

type facetIndex struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
}

var (
	urlPattern = regexp.MustCompile(`https?://[^\s<>"]+`)
	// Hashtags start the text or follow whitespace; a pure-digit tag is not one.
	tagPattern = regexp.MustCompile(`(?:^|\s)(#[\p{L}\p{N}_]*[\p{L}_][\p{L}\p{N}_]*)`)
)

// maxTagGraphemes is the lexicon limit of a facet tag value.
const maxTagGraphemes = 64

// buildFacets returns link and hashtag facets for text, ordered by position.
func buildFacets(text string) []postFacet {
	var facets []postFacet
	for _, loc := range urlPattern.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[0]+len(trimURLTail(text[loc[0]:loc[1]]))
		facets = append(facets, postFacet{
			Index:    facetIndex{ByteStart: start, ByteEnd: end},
			Features: []facetFeature{{Type: facetTypeLink, URI: text[start:end]}},
		})
	}
	for _, m := range tagPattern.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		if insideLink(facets, start) {
			continue
		}
		tag := text[start+1 : end]
		if countGraphemes(tag) > maxTagGraphemes {
			continue
		}
		facets = append(facets, postFacet{
			Index:    facetIndex{ByteStart: start, ByteEnd: end},
			Features: []facetFeature{{Type: facetTypeTag, Tag: tag}},
		})
	}
	sortFacets(facets)
	return facets
}

// trimURLTail drops sentence punctuation that a URL regexp swallows, and a
// closing bracket without its opener.
func trimURLTail(u string) string {
	for len(u) > 0 {
		last := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?'", last) >= 0:
			u = u[:len(u)-1]
		case last == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

func insideLink(facets []postFacet, pos int) bool {
	for _, f := range facets {
		if pos >= f.Index.ByteStart && pos < f.Index.ByteEnd {
			return true
		}
	}
	return false
}

func sortFacets(facets []postFacet) {
	for i := 1; i < len(facets); i++ {
		for j := i; j > 0 && facets[j].Index.ByteStart < facets[j-1].Index.ByteStart; j-- {
			facets[j], facets[j-1] = facets[j-1], facets[j]
		}
	}
}
