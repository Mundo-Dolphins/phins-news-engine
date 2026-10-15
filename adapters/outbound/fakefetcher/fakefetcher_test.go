package fakefetcher_test

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/fakefetcher"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

func TestFetcher_Fetch_ReturnsConfiguredArticles(t *testing.T) {
	want := []article.Article{{ID: "a1"}, {ID: "a2"}}
	f := fakefetcher.New(want)

	got, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Fetch() returned %d articles, want %d", len(got), len(want))
	}
}

func TestFetcher_Fetch_RespectsCancelledContext(t *testing.T) {
	f := fakefetcher.New([]article.Article{{ID: "a1"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("Fetch() error = nil, want context.Canceled")
	}
}
