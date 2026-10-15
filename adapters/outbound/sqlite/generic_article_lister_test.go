// adapters/outbound/sqlite/generic_article_lister_test.go
package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func TestGenericArticleLister_ListGeneric(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	mustSaveArticleWithPublishedAt(t, articles, "old", base.Add(-48*time.Hour))
	mustSaveArticleWithPublishedAt(t, articles, "g1", base)
	mustSaveArticleWithPublishedAt(t, articles, "g2", base.Add(time.Hour))
	mustSaveArticleWithPublishedAt(t, articles, "inj", base.Add(2*time.Hour))
	for id, typ := range map[string]eventsignals.EventType{
		"old": eventsignals.EventTypeGeneric, "g1": eventsignals.EventTypeGeneric,
		"g2": eventsignals.EventTypeGeneric, "inj": eventsignals.EventTypeInjury,
	} {
		rep := sampleRepresentation(id)
		rep.EventType = typ
		if err := clusters.SaveRepresentation(ctx, rep, "rules-v1", base); err != nil {
			t.Fatal(err)
		}
	}
	if err := clusters.CreateCluster(ctx, "c1", "g2", clustering.StatusClosed, base); err != nil {
		t.Fatal(err)
	}

	l := sqliteadapter.NewGenericArticleLister(db)
	got, err := l.ListGeneric(ctx, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Article.ID != "g2" || got[1].Article.ID != "g1" {
		t.Fatalf("got %+v", got)
	}
	if got[0].ClusterID != "c1" || got[1].ClusterID != "" {
		t.Errorf("cluster ids = %q, %q", got[0].ClusterID, got[1].ClusterID)
	}
	one, _ := l.ListGeneric(ctx, base.Add(-time.Hour), 1)
	if len(one) != 1 {
		t.Errorf("limit not applied: %d", len(one))
	}
}

func TestGenericArticleLister_ListTyped(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	mustSaveArticleWithPublishedAt(t, articles, "old", base.Add(-48*time.Hour))
	mustSaveArticleWithPublishedAt(t, articles, "g1", base)
	mustSaveArticleWithPublishedAt(t, articles, "tx", base.Add(time.Hour))
	mustSaveArticleWithPublishedAt(t, articles, "inj", base.Add(2*time.Hour))
	for id, typ := range map[string]eventsignals.EventType{
		"old": eventsignals.EventTypeTransaction, "g1": eventsignals.EventTypeGeneric,
		"tx": eventsignals.EventTypeTransaction, "inj": eventsignals.EventTypeInjury,
	} {
		rep := sampleRepresentation(id)
		rep.EventType = typ
		if err := clusters.SaveRepresentation(ctx, rep, "rules-v1", base); err != nil {
			t.Fatal(err)
		}
	}
	l := sqliteadapter.NewGenericArticleLister(db)
	got, err := l.ListTyped(ctx, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Article.ID != "inj" || got[1].Article.ID != "tx" {
		t.Fatalf("got %+v; want the typed articles in the window, newest first", got)
	}
	if one, _ := l.ListTyped(ctx, base.Add(-time.Hour), 1); len(one) != 1 || one[0].Article.ID != "inj" {
		t.Errorf("limit not applied: %+v", one)
	}
}
