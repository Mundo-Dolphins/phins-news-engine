// Package editorial scores the editorial quality of an article using an
// offline-trained linear model. All four candidate training algorithms
// (Naive Bayes, logistic regression, linear SVM, NB-SVM — see
// application/editorialtrain) reduce at inference time to the same
// bias+weighted-sum shape, so this package only ever needs one scoring
// function regardless of which algorithm produced the promoted model.
package editorial

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// FeatureVector is a sparse bag of named numeric features, shared verbatim
// between training (application/editorialtrain) and runtime (Evaluate) —
// the single source of truth for what a feature is called and how it's
// computed, so train/serve skew is impossible.
type FeatureVector map[string]float64

// Thresholds are the two score boundaries (on Score.Value's 0-100 scale)
// that carve a Model's prediction into reject/ambiguous/accept zones.
type Thresholds struct {
	Reject float64 // Value <= Reject -> ZoneReject
	Accept float64 // Value >= Accept -> ZoneAccept
}

// Zone is Predict's decision-zone verdict.
type Zone string

const (
	ZoneReject    Zone = "reject"
	ZoneAmbiguous Zone = "ambiguous"
	ZoneAccept    Zone = "accept"
)

// Model is the promoted (or candidate) linear editorial-quality classifier
// artifact — whichever of the four offline-trained algorithms won,
// reduced to its runtime-uniform linear form.
type Model struct {
	Version              string
	Algorithm            string // "naive_bayes" | "logistic_regression" | "linear_svm" | "nbsvm"
	FeatureSchemaVersion string
	Bias                 float64
	Weights              map[string]float64
	Thresholds           Thresholds
	// BinaryFeatures makes Predict score feature presence (any non-zero
	// value counts as 1) instead of the raw value — set by a Naive Bayes
	// model trained on presence, so inference matches training.
	BinaryFeatures bool
	// Checksum is the promoted artifact's manifest.json artifact_hash.
	// editorialmodel.Load reads the model itself and does not populate
	// it; the composition root (engine/run.go) sets it from
	// the verified manifest after the artifact-hash check passes. It is
	// persisted onto every editorial decision event as model_checksum,
	// so an audited decision can be traced back to the exact artifact
	// that produced it. Zero value "" when no model is loaded
	// (editorial.mode: disabled) or when a caller loads a model without
	// a manifest (e.g. the pipelinereplay lab tool).
	Checksum string
}

// Score is the editorial-quality assessment of an article.
type Score struct {
	Value        float64 // 0-100, monotonic bounded transform of Raw
	Raw          float64 // pre-squash linear decision value: Bias + Σ Weights[f]*fv[f]
	Zone         Zone
	ModelVersion string
	Placeholder  bool // true only when no model is loaded (Evaluate called with m == nil)
}

// NeutralScore is Evaluate's placeholder value when no model is loaded.
const NeutralScore = 50

// Predict scores fv against m: Raw = m.Bias + Σ m.Weights[f]*fv[f], using 0
// for any fv feature absent from m.Weights and vice versa — this is what
// makes an unknown feature a silent no-op instead of an error. Value is a
// monotonic bounded transform of Raw onto [0,100]. The rational squash
// preserves ordering for large-magnitude NB scores where a logistic would
// round distinct values to exactly 0 or 100. It is a ranking score, not a
// calibrated probability. Deterministic, no I/O.
func Predict(m Model, fv FeatureVector) Score {
	raw := m.Bias
	for name, weight := range m.Weights {
		v := fv[name]
		if m.BinaryFeatures && v != 0 {
			v = 1
		}
		raw += weight * v
	}
	value := 50 + 50*(raw/(1+math.Abs(raw)))
	// The fallback thresholds {Reject: 0, Accept: 100} mean "all
	// ambiguous". Keep finite scores strictly inside those boundaries.
	if value <= 0 {
		value = math.Nextafter(0, 100)
	} else if value >= 100 {
		value = math.Nextafter(100, 0)
	}

	zone := ZoneAmbiguous
	switch {
	case value >= m.Thresholds.Accept:
		zone = ZoneAccept
	case value <= m.Thresholds.Reject:
		zone = ZoneReject
	}

	return Score{Value: value, Raw: raw, Zone: zone, ModelVersion: m.Version}
}

// Evaluate is ProcessCycle's entrypoint. m == nil reproduces the exact
// pre-Phase-7 placeholder behavior (NeutralScore, Placeholder: true,
// Zone: "") — editorial.mode: disabled never loads a model, so this is
// the only path reachable in that mode.
func Evaluate(a article.Article, isPrimarySource bool, m *Model) Score {
	if m == nil {
		return Score{Value: NeutralScore, Placeholder: true}
	}
	return Predict(*m, ExtractFeatures(a, isPrimarySource))
}

var (
	wordSplitPattern = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	hashtagPattern   = regexp.MustCompile(`#\w+`)
	mentionPattern   = regexp.MustCompile(`@\w+`)
	urlPattern       = regexp.MustCompile(`https?://([^/\s]+)`)
)

// bodyTokenWeight down-weights raw body-token counts relative to
// title/summary n-grams, since a full article body is far longer than a
// headline and would otherwise dominate feature magnitude by sheer
// volume. This value is provisional — recalibrating it against the
// promoted model's actual training data is out of scope here and
// belongs to the #157/#168 recalibration work; this task only makes the
// signal available under a namespace the current model's Weights map
// doesn't contain, so it changes nothing until that recalibration adds
// these keys.
const bodyTokenWeight = 0.25

// tokenize is ExtractFeatures' own minimal, config-free tokenizer:
// lowercase, split on runs of non-letter/non-digit runes. It deliberately
// does not reuse normalization.Normalize (which needs a TextConfig,
// infrastructure-loaded config) — ExtractFeatures must be callable
// identically, with zero configuration, from both training and runtime.
func tokenize(s string) []string {
	lower := strings.ToLower(s)
	raw := wordSplitPattern.Split(lower, -1)
	tokens := make([]string, 0, len(raw))
	for _, t := range raw {
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

func charNgrams(s string, n int) []string {
	runes := []rune(strings.ToLower(s))
	if len(runes) < n {
		return nil
	}
	grams := make([]string, 0, len(runes)-n+1)
	for i := 0; i+n <= len(runes); i++ {
		grams = append(grams, string(runes[i:i+n]))
	}
	return grams
}

func capsRatio(s string) float64 {
	letters, upper := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(upper) / float64(letters)
}

// ExtractFeatures builds the full, unpruned feature candidate set for a —
// shared verbatim by editorialtrain (offline) and Evaluate (runtime), so
// training and inference can never disagree about what a feature means.
// isPrimarySource is caller-supplied (this package has no source-trust
// data of its own) — the same trusted-source signal ProcessCycle already
// resolves for classification bypass and representative selection.
func ExtractFeatures(a article.Article, isPrimarySource bool) FeatureVector {
	fv := FeatureVector{}

	titleTokens := tokenize(a.Title)
	bodyTokens := tokenize(a.Summary)
	allTokens := append(append([]string{}, titleTokens...), bodyTokens...)

	for _, tok := range allTokens {
		fv["uni:"+tok]++
	}
	for i := 0; i+1 < len(allTokens); i++ {
		fv["bi:"+allTokens[i]+"_"+allTokens[i+1]]++
	}
	for _, n := range []int{3, 4, 5} {
		for _, g := range charNgrams(a.Title+" "+a.Summary, n) {
			fv[fmt.Sprintf("char%d:%s", n, g)]++
		}
	}

	fv["len:title_tokens"] = float64(len(titleTokens))
	fv["len:body_tokens"] = float64(len(bodyTokens))

	if a.Source.ID != (source.ID{}) {
		fv["meta:source_id:"+a.Source.ID.String()] = 1
	}
	if a.Source.Type != "" {
		fv["meta:source_type:"+string(a.Source.Type)] = 1
	}
	if a.Domain != "" {
		fv["meta:domain:"+a.Domain] = 1
	}
	if a.ContentType != "" {
		fv["meta:content_type:"+string(a.ContentType)] = 1
	}

	combined := a.Title + " " + a.Summary
	fv["count:hashtags"] = float64(len(hashtagPattern.FindAllString(combined, -1)))
	fv["count:mentions"] = float64(len(mentionPattern.FindAllString(combined, -1)))
	fv["ratio:caps"] = capsRatio(a.Summary)

	fv["flag:external_link"] = 0
	for _, m := range urlPattern.FindAllStringSubmatch(a.Summary, -1) {
		host := strings.ToLower(m[1])
		if a.Domain == "" || !strings.HasSuffix(host, strings.ToLower(a.Domain)) {
			fv["flag:external_link"] = 1
			break
		}
	}

	if !a.PublishedAt.IsZero() && !a.FetchedAt.IsZero() {
		fv["age:hours"] = a.FetchedAt.Sub(a.PublishedAt).Hours()
	}

	if isPrimarySource {
		fv["flag:primary_source"] = 1
	} else {
		fv["flag:primary_source"] = 0
	}

	for k, v := range lexiconFeatures(combined) {
		fv[k] = v
	}

	if a.Body != "" {
		bodyFullTokens := tokenize(a.Body)
		for _, tok := range bodyFullTokens {
			fv["bodytok:"+tok] += bodyTokenWeight
		}
		for i := 0; i+1 < len(bodyFullTokens); i++ {
			fv["bodybi:"+bodyFullTokens[i]+"_"+bodyFullTokens[i+1]] += bodyTokenWeight
		}
		fv["len:body_full_tokens"] = float64(len(bodyFullTokens))
	}

	return fv
}
