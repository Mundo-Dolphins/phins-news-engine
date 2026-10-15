// app/entitycycle/lister_test.go
package entitycycle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

type fakeFinder map[string]article.Article

func (f fakeFinder) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f[id]
	if !ok {
		return article.Article{}, fmt.Errorf("find %s: %w", id, article.ErrNotFound)
	}
	return a, nil
}

type fakeProcessed map[string]bool

func (f fakeProcessed) FilterUnprocessed(_ context.Context, _ string, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if !f[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

var t0 = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)

func arts() fakeFinder {
	return fakeFinder{
		"a1": {ID: "a1", Title: "one", FetchedAt: t0.Add(2 * time.Hour)},
		"a2": {ID: "a2", Title: "two", FetchedAt: t0.Add(1 * time.Hour)},
		"a3": {ID: "a3", Title: "three", FetchedAt: t0.Add(3 * time.Hour)},
		"zz": {ID: "zz", Title: "not in the session", FetchedAt: t0},
	}
}

func ids(as []article.Article) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.ID
	}
	return out
}

func TestSessionLister_OnlySessionArticlesOldestFirst(t *testing.T) {
	l := entitycycle.SessionLister{IDs: []string{"a1", "a2", "a3"}, Articles: arts(), Processed: fakeProcessed{}}
	got, err := l.ListUnprocessedByEntityDiscovery(context.Background(), time.Time{}, "v1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(got)) != "[a2 a1 a3]" {
		t.Fatalf("got %v, want the session's articles ordered by fetched_at", ids(got))
	}
}

func TestSessionLister_ExcludesProcessedAndHonoursTheLimit(t *testing.T) {
	l := entitycycle.SessionLister{IDs: []string{"a1", "a2", "a3"}, Articles: arts(), Processed: fakeProcessed{"a2": true}}
	got, _ := l.ListUnprocessedByEntityDiscovery(context.Background(), time.Time{}, "v1", 1)
	if fmt.Sprint(ids(got)) != "[a1]" {
		t.Fatalf("got %v, want the oldest unprocessed one only", ids(got))
	}
	all, _ := l.ListSince(context.Background(), time.Time{}, 100)
	if len(all) != 3 {
		t.Fatalf("ListSince ignores the processed marker (that is --force): got %v", ids(all))
	}
}

func TestSessionLister_SkipsPurgedArticlesAndDuplicatesAndEmptySession(t *testing.T) {
	l := entitycycle.SessionLister{IDs: []string{"a1", "gone", "a1"}, Articles: arts(), Processed: fakeProcessed{}}
	got, err := l.ListUnprocessedByEntityDiscovery(context.Background(), time.Time{}, "v1", 10)
	if err != nil || fmt.Sprint(ids(got)) != "[a1]" {
		t.Fatalf("got %v, %v; a purged article is skipped and a duplicate id appears once", ids(got), err)
	}
	empty := entitycycle.SessionLister{Articles: arts(), Processed: fakeProcessed{}}
	if got, err := empty.ListUnprocessedByEntityDiscovery(context.Background(), time.Time{}, "v1", 10); err != nil || len(got) != 0 {
		t.Fatalf("empty session: %v, %v", got, err)
	}
}
