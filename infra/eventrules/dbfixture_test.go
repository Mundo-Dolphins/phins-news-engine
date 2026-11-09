// infra/eventrules/dbfixture_test.go
package eventrules

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "t.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedArticles stores articles whose representation has event type typ;
// titles[i] gets id <prefix><i>.
func seedArticles(t *testing.T, db *sql.DB, prefix string, typ eventsignals.EventType, titles ...string) {
	t.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	now := time.Now().UTC()
	sid, err := source.NewID(source.TypeRSS, "src")
	if err != nil {
		t.Fatal(err)
	}
	for i, title := range titles {
		id := fmt.Sprintf("%s%d", prefix, i)
		a := article.Article{
			ID: id, Title: title, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: sid, Type: source.TypeRSS},
			FetchedAt: now, PublishedAt: now.Add(-time.Duration(i) * time.Hour), Status: article.StatusFetched,
		}
		if err := articles.Save(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		rep := eventsignals.Representation{ArticleID: id, SourceID: "src", EventType: typ, PublishedAt: a.PublishedAt, FetchedAt: now}
		if err := clusters.SaveRepresentation(context.Background(), rep, "rules-v1", now); err != nil {
			t.Fatal(err)
		}
	}
}

// seedProposals stores ps (moving non-pending ones to their status) and
// returns their ids.
func seedProposals(t *testing.T, db *sql.DB, ps ...eventruleproposal.Proposal) []string {
	t.Helper()
	repo := sqliteeventruleproposal.NewRepository(db)
	var ids []string
	for _, p := range ps {
		p.CreatedAt = time.Now().UTC()
		got, err := repo.Upsert(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != eventruleproposal.StatusPending {
			got.Status = p.Status
			if err := repo.Save(context.Background(), got); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, got.ID)
	}
	return ids
}
