package editorial_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// mustNewID builds a source.ID for a fixed, always-valid fixture literal;
// baseArticle (below) is called from many places that don't carry a
// *testing.T, so this panics rather than threading one through every call
// site for an error path that can never fire.
func mustNewID(platform source.Type, raw string) source.ID {
	id, err := source.NewID(platform, raw)
	if err != nil {
		panic(err)
	}
	return id
}

func baseArticle() article.Article {
	published := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return article.Article{
		Title:       "Isotopes sign RB to a two year contract extension",
		Summary:     "The Springfield Isotopes announced today they signed a running back. Details at 5pm.",
		Source:      source.Source{ID: mustNewID(source.TypeRSS, "src-1"), Type: source.TypeRSS},
		Domain:      "isotopes.example.com",
		ContentType: article.ContentTypeArticle,
		PublishedAt: published,
		FetchedAt:   published.Add(2 * time.Hour),
	}
}

func TestExtractFeatures_Unigrams(t *testing.T) {
	fv := editorial.ExtractFeatures(baseArticle(), false)
	if fv["uni:isotopes"] == 0 {
		t.Error("expected a unigram feature for 'isotopes'")
	}
	if fv["uni:contract"] == 0 {
		t.Error("expected a unigram feature for 'contract'")
	}
}

func TestExtractFeatures_Bigrams(t *testing.T) {
	fv := editorial.ExtractFeatures(baseArticle(), false)
	if fv["bi:contract_extension"] == 0 {
		t.Error("expected a bigram feature for 'contract extension'")
	}
}

func TestExtractFeatures_CharNgrams(t *testing.T) {
	fv := editorial.ExtractFeatures(baseArticle(), false)
	found := false
	for k := range fv {
		if len(k) > 6 && k[:6] == "char3:" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected at least one char3: n-gram feature")
	}
}

func TestExtractFeatures_LengthSourceDomainContentType(t *testing.T) {
	fv := editorial.ExtractFeatures(baseArticle(), false)
	if fv["len:title_tokens"] == 0 {
		t.Error("expected len:title_tokens to be non-zero")
	}
	if fv["len:body_tokens"] == 0 {
		t.Error("expected len:body_tokens to be non-zero")
	}
	if fv["meta:source_id:rss:src-1"] != 1 {
		t.Errorf("meta:source_id:rss:src-1 = %v, want 1", fv["meta:source_id:rss:src-1"])
	}
	if fv["meta:source_type:rss"] != 1 {
		t.Errorf("meta:source_type:rss = %v, want 1", fv["meta:source_type:rss"])
	}
	if fv["meta:domain:isotopes.example.com"] != 1 {
		t.Errorf("meta:domain:isotopes.example.com = %v, want 1", fv["meta:domain:isotopes.example.com"])
	}
	if fv["meta:content_type:article"] != 1 {
		t.Errorf("meta:content_type:article = %v, want 1", fv["meta:content_type:article"])
	}
}

func TestExtractFeatures_HashtagsMentionsCapsRatio(t *testing.T) {
	a := baseArticle()
	a.Summary = "BREAKING: #FinsUp #Isotopes cc @someuser THIS IS HUGE NEWS"
	fv := editorial.ExtractFeatures(a, false)
	if fv["count:hashtags"] != 2 {
		t.Errorf("count:hashtags = %v, want 2", fv["count:hashtags"])
	}
	if fv["count:mentions"] != 1 {
		t.Errorf("count:mentions = %v, want 1", fv["count:mentions"])
	}
	if fv["ratio:caps"] <= 0 {
		t.Errorf("ratio:caps = %v, want > 0 for a mostly-uppercase summary", fv["ratio:caps"])
	}
}

func TestExtractFeatures_ExternalLink(t *testing.T) {
	withLink := baseArticle()
	withLink.Domain = "isotopes.example.com"
	withLink.Summary = "Full story at https://sportsnet.example.com/nfl/story/123"
	fv := editorial.ExtractFeatures(withLink, false)
	if fv["flag:external_link"] != 1 {
		t.Errorf("flag:external_link = %v, want 1 (sportsnet.example.com != isotopes.example.com)", fv["flag:external_link"])
	}

	withoutLink := baseArticle()
	fv2 := editorial.ExtractFeatures(withoutLink, false)
	if fv2["flag:external_link"] != 0 {
		t.Errorf("flag:external_link = %v, want 0 (no URL in body)", fv2["flag:external_link"])
	}
}

func TestExtractFeatures_AgeHours(t *testing.T) {
	fv := editorial.ExtractFeatures(baseArticle(), false)
	if fv["age:hours"] != 2 {
		t.Errorf("age:hours = %v, want 2 (FetchedAt is 2h after PublishedAt)", fv["age:hours"])
	}
}

func TestExtractFeatures_PrimarySourceFlag(t *testing.T) {
	fvPrimary := editorial.ExtractFeatures(baseArticle(), true)
	fvNot := editorial.ExtractFeatures(baseArticle(), false)
	if fvPrimary["flag:primary_source"] != 1 {
		t.Errorf("flag:primary_source = %v, want 1", fvPrimary["flag:primary_source"])
	}
	if fvNot["flag:primary_source"] != 0 {
		t.Errorf("flag:primary_source = %v, want 0", fvNot["flag:primary_source"])
	}
}

func TestExtractFeatures_EmptyArticleDoesNotPanic(t *testing.T) {
	fv := editorial.ExtractFeatures(article.Article{}, false)
	if fv == nil {
		t.Error("expected a non-nil (possibly empty) FeatureVector")
	}
}

func TestExtractFeatures_Deterministic(t *testing.T) {
	a := baseArticle()
	fv1 := editorial.ExtractFeatures(a, true)
	fv2 := editorial.ExtractFeatures(a, true)
	if len(fv1) != len(fv2) {
		t.Fatalf("len mismatch: %d vs %d", len(fv1), len(fv2))
	}
	for k, v := range fv1 {
		if fv2[k] != v {
			t.Errorf("feature %q: %v vs %v, want identical across calls", k, v, fv2[k])
		}
	}
}
