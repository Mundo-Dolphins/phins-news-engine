package entities_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

// queueItems decodes the items of an entities.review_queue result.
func queueItems(t *testing.T, res action.Result) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.Object)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

// as asserts v to T, yielding the zero value when it is another type.
func as[T any](v any) T {
	t, _ := v.(T)
	return t
}

func TestReviewQueueOrderAndFields(t *testing.T) {
	f := newFixture(t)
	low := f.seed(entityproposal.Proposal{CanonicalName: "Quill Farrowby", Occurrences: 1, Confidence: 0.5})
	high := f.seed(entityproposal.Proposal{
		CanonicalName: "Zeno Ashbury", Occurrences: 9, Confidence: 0.9, Aliases: []string{"Z. Ashbury"},
		Kind: entityproposal.KindPossibleDuplicate, TargetEntityID: "jordan-sample",
		Metadata: map[string]string{
			entitydiscovery.MetadataVerifyAttemptedAt: "2026-11-01T00:00:00Z",
			"position": "QB", "role": "starter", "team": "Springfield Isotopes", "league": "NFL",
			"suggested_entity": "Jordan Sample", "suggested_team": "Springfield", "suggested_basis": "surname",
			"suggested_candidates": "A; B", "note": "kept",
		},
		WebSources: []entityproposal.WebSource{{Domain: "example.org", URL: "https://example.org/z", Title: "Zeno page"}},
	})
	f.seed(entityproposal.Proposal{CanonicalName: "Orrin Pellwick", Occurrences: 20, Status: entityproposal.StatusRejected})

	res, err := f.run("entities.review_queue", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "2 pending entity proposal(s)" {
		t.Fatalf("summary = %q", res.Summary)
	}
	items := queueItems(t, res)
	if len(items) != 2 || items[0]["id"] != high || items[1]["id"] != low {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	if it["name"] != "Zeno Ashbury" || it["type"] != "player" || it["kind"] != "possible_duplicate" ||
		it["target_entity_id"] != "jordan-sample" || it["occurrences"] != float64(9) || it["confidence"] != 0.9 {
		t.Fatalf("item = %+v", it)
	}
	if it["affiliation"] != "" {
		t.Fatalf("affiliation = %v", it["affiliation"])
	}
	meta, _ := it["metadata"].(map[string]any)
	if _, has := meta[entitydiscovery.MetadataVerifyAttemptedAt]; has || meta["note"] != "kept" {
		t.Fatalf("metadata = %+v", meta)
	}
	ver, _ := it["verification"].(map[string]any)
	if ver["position"] != "QB" || ver["role"] != "starter" || ver["team"] != "Springfield Isotopes" || ver["league"] != "NFL" {
		t.Fatalf("verification = %+v", ver)
	}
	sug, _ := it["suggestion"].(map[string]any)
	if sug["entity"] != "Jordan Sample" || sug["team"] != "Springfield" || sug["basis"] != "surname" || sug["candidates"] != "A; B" {
		t.Fatalf("suggestion = %+v", sug)
	}
	ws, _ := as[[]any](it["web_sources"])[0].(map[string]any)
	if ws["domain"] != "example.org" || ws["url"] != "https://example.org/z" || ws["title"] != "Zeno page" {
		t.Fatalf("web_sources = %+v", ws)
	}
	if aliases := as[[]any](it["aliases"]); len(aliases) != 1 || aliases[0] != "Z. Ashbury" {
		t.Fatalf("aliases = %+v", aliases)
	}
	if items[1]["affiliation"] != "UNKNOWN" {
		t.Fatalf("player without team affiliation = %v", items[1]["affiliation"])
	}
	if it["first_seen"] == "" || it["last_seen"] == "" {
		t.Fatalf("dates = %v %v", it["first_seen"], it["last_seen"])
	}
}

func TestReviewQueueLimit(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.seed(entityproposal.Proposal{CanonicalName: fmt.Sprintf("Candidate Number%c", 'A'+i), Occurrences: 3 - i})
	}
	res, err := f.run("entities.review_queue", `{"limit":2}`)
	if err != nil || len(queueItems(t, res)) != 2 {
		t.Fatalf("limited = %+v, %v", res.Object, err)
	}
	for _, bad := range []string{`{"limit":0}`, `{"limit":501}`} {
		if _, err := f.run("entities.review_queue", bad); err == nil {
			t.Fatalf("%s: expected invalid params", bad)
		} else {
			wantCode(t, err, action.CodeInvalidParams)
		}
	}
}

func TestReviewQueueEvidenceURL(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	src := source.Source{ID: testsupport.MustID(t, source.TypeWeb, "example-com"), Name: "Example", Type: source.TypeWeb}
	if err := sqliteadapter.NewArticleRepository(f.db).Save(ctx, article.Article{
		ID: "kept", OriginalURL: "https://example.com/kept", CanonicalURL: "https://example.com/kept",
		Source: src, Title: "Kept story", Summary: "s", FetchedAt: now, Status: article.StatusFetched,
	}); err != nil {
		t.Fatal(err)
	}
	f.seed(entityproposal.Proposal{CanonicalName: "Zeno Ashbury", Sources: []entityproposal.Source{
		{ArticleID: "kept", ArticleTitle: "Kept story"},
		{ArticleID: "purged", ArticleTitle: "Purged story"},
		{ArticleID: "x3", ArticleTitle: "Three"}, {ArticleID: "x4", ArticleTitle: "Four"},
	}})
	res, err := f.run("entities.review_queue", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	ev := as[[]any](queueItems(t, res)[0]["evidence"])
	if len(ev) != 4 {
		t.Fatalf("evidence has %d entries, want all 4", len(ev))
	}
	byID := map[string]map[string]any{}
	for _, e := range ev {
		m := as[map[string]any](e)
		byID[as[string](m["article_id"])] = m
	}
	if byID["kept"]["url"] != "https://example.com/kept" || byID["kept"]["title"] != "Kept story" {
		t.Fatalf("kept = %+v", byID["kept"])
	}
	if byID["purged"]["url"] != "" || byID["purged"]["title"] != "Purged story" {
		t.Fatalf("purged = %+v", byID["purged"])
	}
}

func TestCatalogSearchAccentsAndCase(t *testing.T) {
	f := newFixture(t)
	write(t, f.manual, manualYAML+`  - id: jose-example
    canonical_name: José Example
    kind: player
    team_id: springfield-isotopes
    terms: [José Example, Pepe]
`)
	for _, q := range []string{"jose", "JOSÉ exam", "pepe"} {
		res, err := f.run("entities.catalog_search", fmt.Sprintf(`{"query":%q}`, q))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res.Object)
		var out struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || out.Results[0]["id"] != "jose-example" || out.Results[0]["name"] != "José Example" ||
			out.Results[0]["type"] != "player" || out.Results[0]["team"] != "springfield-isotopes" {
			t.Fatalf("query %q results = %+v", q, out.Results)
		}
		if terms := as[[]any](out.Results[0]["terms"]); len(terms) != 2 {
			t.Fatalf("terms = %+v", terms)
		}
	}
}

func TestCatalogSearchEmptyQueryInvalid(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{`{}`, `{"query":""}`, `{"query":"   "}`} {
		_, err := f.run("entities.catalog_search", p)
		wantCode(t, err, action.CodeInvalidParams)
	}
}

func TestCatalogSearchLimit(t *testing.T) {
	f := newFixture(t)
	res, err := f.run("entities.catalog_search", `{"query":"s","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res.Object)
	var out struct {
		Results []map[string]any `json:"results"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Results) != 1 {
		t.Fatalf("results = %+v", out.Results)
	}
	for _, bad := range []string{`{"query":"s","limit":0}`, `{"query":"s","limit":101}`} {
		_, err := f.run("entities.catalog_search", bad)
		wantCode(t, err, action.CodeInvalidParams)
	}
}
