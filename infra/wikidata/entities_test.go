package wikidata

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

const malloryEntity = `{
 "id":"Q1",
 "labels":{"en":{"language":"en","value":"Patrick Ostrander"},"es":{"language":"es","value":"Patrick Ostrander ES"}},
 "descriptions":{"en":{"language":"en","value":"American football quarterback"}},
 "aliases":{"en":[{"language":"en","value":"Ostrander"},{"language":"en","value":"Pat Ostrander II"}]},
 "claims":{
  "P54":[
   {"mainsnak":{"snaktype":"value","property":"P54","datavalue":{"type":"wikibase-entityid","value":{"id":"Q910000008"}}},
    "rank":"normal",
    "qualifiers":{
      "P580":[{"snaktype":"value","property":"P580","datavalue":{"type":"time","value":{"time":"+2017-01-01T00:00:00Z","precision":9}}}],
      "P582":[{"snaktype":"novalue","property":"P582"}],
      "P9999":[{"snaktype":"value","property":"P9999","datavalue":{"type":"string","value":"dropped"}}]}},
   {"mainsnak":{"snaktype":"value","property":"P54","datavalue":{"type":"wikibase-entityid","value":{"id":"Q111"}}},
    "rank":"deprecated",
    "qualifiers":{
      "P580":[{"snaktype":"value","property":"P580","datavalue":{"type":"time","value":{"time":"+2010-04-29T00:00:00Z","precision":11}}}],
      "P582":[{"snaktype":"value","property":"P582","datavalue":{"type":"time","value":{"time":"+2012-00-00T00:00:00Z","precision":9}}}]}}
  ],
  "P106":[{"mainsnak":{"snaktype":"value","property":"P106","datavalue":{"type":"wikibase-entityid","value":{"id":"Q19204627"}}},"rank":"preferred"}],
  "P569":[{"mainsnak":{"snaktype":"value","property":"P569","datavalue":{"type":"time","value":{"time":"+1995-09-17T00:00:00Z","precision":11}}},"rank":"normal"}],
  "P286":[{"mainsnak":{"snaktype":"somevalue","property":"P286"},"rank":"normal","qualifiers":{"P582":[{"snaktype":"somevalue","property":"P582"}]}}],
  "P27":[{"mainsnak":{"snaktype":"value","property":"P27","datavalue":{"type":"time","value":{"time":"-0500-00-00T00:00:00Z","precision":9}}},"rank":"weird"}]
 }}`

func entitiesHandler(entities map[string]string, calls *atomic.Int32, seenIDs *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		q := r.URL.Query()
		if q.Get("action") != "wbgetentities" || q.Get("format") != "json" || q.Get("languages") != "en" {
			http.Error(w, "bad params", http.StatusBadRequest)
			return
		}
		if !strings.Contains(q.Get("props"), "claims") {
			http.Error(w, "missing props", http.StatusBadRequest)
			return
		}
		ids := strings.Split(q.Get("ids"), "|")
		if seenIDs != nil {
			*seenIDs = append(*seenIDs, ids...)
		}
		var parts []string
		for _, id := range ids {
			if body, ok := entities[id]; ok {
				parts = append(parts, fmt.Sprintf("%q:%s", id, body))
			} else {
				parts = append(parts, fmt.Sprintf(`%q:{"id":%q,"missing":""}`, id, id))
			}
		}
		_, _ = w.Write([]byte(`{"entities":{` + strings.Join(parts, ",") + `},"success":1}`))
	}
}

func TestGet_MapsLabelsAliasesDescriptionsAndStatements(t *testing.T) {
	c, _ := newTestClient(t, entitiesHandler(map[string]string{"Q1": malloryEntity}, nil, nil), nil)

	got, err := c.Get(context.Background(), []string{"Q1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	e := got[0]
	if e.ID != "Q1" || e.CanonicalID != "" || e.Label != "Patrick Ostrander" || e.Description != "American football quarterback" {
		t.Fatalf("entity header = %+v", e)
	}
	if len(e.Aliases) != 2 || e.Aliases[0] != "Ostrander" {
		t.Errorf("Aliases = %v", e.Aliases)
	}

	// Allow-list order is P31 P641 P106 P54 P413 P286 P118 P27; P569 is not allowed.
	var props []string
	for _, s := range e.Statements {
		props = append(props, s.Property)
	}
	if strings.Join(props, ",") != "P106,P54,P54,P286,P27" {
		t.Fatalf("statement properties = %v, want P106,P54,P54,P286,P27", props)
	}
	if e.Statements[0].Rank != knowledge.RankPreferred || e.Statements[0].Value.EntityID != "Q19204627" || e.Statements[0].Value.Kind != knowledge.KindEntity {
		t.Errorf("P106 statement = %+v", e.Statements[0])
	}
}

func TestGet_TemporalQualifiersDriveValidity(t *testing.T) {
	c, _ := newTestClient(t, entitiesHandler(map[string]string{"Q1": malloryEntity}, nil, nil), nil)
	got, err := c.Get(context.Background(), []string{"Q1"})
	if err != nil {
		t.Fatal(err)
	}
	st := got[0].Statements
	current, former := st[1], st[2]

	if current.ValidFrom == nil || !current.ValidFrom.Equal(time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("current.ValidFrom = %v, want 2017-01-01 (year precision)", current.ValidFrom)
	}
	if current.ValidTo != nil {
		t.Errorf("novalue end qualifier must leave ValidTo nil, got %v", current.ValidTo)
	}
	if !current.ValidAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("current statement should be valid today")
	}
	if len(current.Qualifiers) != 2 {
		t.Errorf("qualifiers = %+v, want only P580 and P582 (P9999 dropped)", current.Qualifiers)
	}

	// Coarse-precision end "+2012-00-00" must parse (month/day 00 -> 01), and rank is deprecated.
	if former.ValidTo == nil || !former.ValidTo.Equal(time.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("former.ValidTo = %v, want 2012-01-01", former.ValidTo)
	}
	if former.Rank != knowledge.RankDeprecated || former.ValidAt(time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("deprecated statement must never be valid: %+v", former)
	}

	// somevalue end leaves ValidTo nil; unknown rank falls back to normal; BCE time degrades to KindOther.
	if st[3].ValidTo != nil || st[3].Value.Kind != knowledge.KindSomeValue {
		t.Errorf("P286 = %+v", st[3])
	}
	if st[4].Rank != knowledge.RankNormal || st[4].Value.Kind != knowledge.KindOther {
		t.Errorf("P27 = %+v, want normal rank and KindOther for BCE time", st[4])
	}
}

func TestGet_MultiQIDBatchPreservesInputOrderAndOmitsMissing(t *testing.T) {
	var calls atomic.Int32
	var seen []string
	c, _ := newTestClient(t, entitiesHandler(map[string]string{
		"Q1": malloryEntity,
		"Q2": `{"id":"Q2","labels":{"en":{"language":"en","value":"Two"}}}`,
	}, &calls, &seen), nil)

	got, err := c.Get(context.Background(), []string{"Q2", "Q404", "Q1", "Q2"})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1 batched request", calls.Load())
	}
	if len(seen) != 3 {
		t.Errorf("ids sent = %v, want deduplicated [Q2 Q404 Q1]", seen)
	}
	if len(got) != 2 || got[0].ID != "Q2" || got[1].ID != "Q1" {
		t.Fatalf("Get() ids = %v, want [Q2 Q1]", []string{got[0].ID, got[1].ID})
	}
}

func TestGet_ChunksAt50IDs(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, entitiesHandler(nil, &calls, nil), nil)
	ids := make([]string, 120)
	for i := range ids {
		ids[i] = fmt.Sprintf("Q%d", i+1)
	}
	if _, err := c.Get(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("requests = %d, want 3 (50+50+20)", calls.Load())
	}
}

func TestGet_RedirectedIDKeepsRequestedIDAndSetsCanonical(t *testing.T) {
	c, _ := newTestClient(t, entitiesHandler(map[string]string{
		"Q10": `{"id":"Q20","labels":{"en":{"language":"en","value":"Merged"}}}`,
	}, nil, nil), nil)
	got, err := c.Get(context.Background(), []string{"Q10"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	if got[0].ID != "Q10" || got[0].CanonicalID != "Q20" {
		t.Fatalf("ID/CanonicalID = %q/%q, want Q10/Q20", got[0].ID, got[0].CanonicalID)
	}
}

func TestGet_RejectsInvalidIDsWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, entitiesHandler(nil, &calls, nil), nil)
	for _, bad := range []string{"", "ostrander", "Q1|Q2", "P54", "q1"} {
		if _, err := c.Get(context.Background(), []string{bad}); err == nil {
			t.Errorf("Get(%q) error = nil, want invalid-id error", bad)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want 0", calls.Load())
	}
	if got, err := c.Get(context.Background(), nil); err != nil || got != nil {
		t.Errorf("Get(nil) = %v, %v want nil,nil", got, err)
	}
}

func TestGet_CustomPropertyAllowList(t *testing.T) {
	c, _ := newTestClient(t, entitiesHandler(map[string]string{"Q1": malloryEntity}, nil, nil), func(cfg *Config) { cfg.Properties = []string{"P106"} })
	got, err := c.Get(context.Background(), []string{"Q1"})
	if err != nil || len(got[0].Statements) != 1 || got[0].Statements[0].Property != "P106" {
		t.Fatalf("Get() = %+v, %v, want only P106", got, err)
	}
}
