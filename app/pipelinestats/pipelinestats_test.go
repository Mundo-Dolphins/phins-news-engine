package pipelinestats_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/pipelinestats"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeArticles struct{ list []article.Article }

func (f fakeArticles) ListSince(context.Context, time.Time, int) ([]article.Article, error) {
	return f.list, nil
}

type fakeOutbox struct{}

func (fakeOutbox) CountSucceededSince(context.Context, time.Time) (int, error) { return 3, nil }
func (fakeOutbox) StatusCounts(context.Context, time.Time) (map[publishing.OutboxStatus]out.StatusCount, error) {
	return map[publishing.OutboxStatus]out.StatusCount{publishing.OutboxPending: {Count: 5}}, nil
}

func art(id, name string, status article.Status) article.Article {
	return article.Article{ID: id, Source: source.Source{Name: name}, Status: status}
}

func TestBuildMissingLogIsReported(t *testing.T) {
	now := time.Now()
	r, err := pipelinestats.Build(context.Background(), fakeArticles{list: []article.Article{art("a", "A", article.StatusPublished)}}, fakeOutbox{},
		now.Add(-time.Hour), now, filepath.Join(t.TempDir(), "missing.log"), 10)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if r.LogUnavailable == "" {
		t.Error("LogUnavailable is empty, want the open error")
	}
	if r.ArticlesAnalyzed != 1 || r.TelegramDeliveredInWindow != 3 || r.TelegramBacklog["pending"] != 5 {
		t.Errorf("DB counts not filled: %+v", r)
	}
}

func TestBuildTopSourcesOrder(t *testing.T) {
	now := time.Now()
	list := []article.Article{
		art("1", "Zed", article.StatusPublished), art("2", "Alpha", article.StatusPublished),
		art("3", "Mid", article.StatusPublished), art("4", "Mid", article.StatusRejectedQuality),
		art("5", "Mid", article.StatusPublished),
	}
	r, err := pipelinestats.Build(context.Background(), fakeArticles{list: list}, fakeOutbox{}, now.Add(-time.Hour), now, filepath.Join(t.TempDir(), "x.log"), 2)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(r.TopSources) != 2 || r.TopSources[0].Source != "Mid" || r.TopSources[0].Total != 3 || r.TopSources[1].Source != "Alpha" {
		t.Errorf("TopSources = %+v, want Mid(3) then Alpha (tie with Zed broken by name)", r.TopSources)
	}
}
