// Package trainmine implements `train mine`:
// scanning stored articles not yet in Gold for cases where the
// promoted NB model and the LLM teacher strongly disagree.
package trainmine

import "github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"

// ClassifyHardExample compares one article's NB prediction against its
// LLM label and returns "negative" (NB confidently ZoneAccept, LLM
// confidently "discard" — exposes vocabulary that incorrectly triggers
// publication), "positive" (the reverse — reveals vocabulary the
// classifier doesn't know about yet), or "" (agreement, or either side
// not confident enough — not a hard example). nbConfidence and
// llmConfidence are both expected on a [0,1] scale.
func ClassifyHardExample(nbZone editorial.Zone, nbConfidence float64, llmLabel string, llmConfidence float64, minNBConfidence, llmAutoLabelThreshold float64) string {
	if nbConfidence < minNBConfidence || llmConfidence < llmAutoLabelThreshold {
		return ""
	}
	switch {
	case nbZone == editorial.ZoneAccept && llmLabel == "discard":
		return "negative"
	case nbZone == editorial.ZoneReject && llmLabel == "publish":
		return "positive"
	default:
		return ""
	}
}
