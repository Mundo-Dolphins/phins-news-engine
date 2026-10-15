// adapters/outbound/sqliteeventruleproposal/repository_test.go

package sqliteeventruleproposal_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sqliteeventruleproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqliteeventruleproposal-test-template-")
	if err != nil {
		panic("create SQLite test template directory: " + err.Error())
	}

	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		_ = os.RemoveAll(templateDir)
		panic("open SQLite test template: " + err.Error())
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("migrate SQLite test template: " + err.Error())
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("checkpoint SQLite test template: " + err.Error())
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(templateDir)
		panic("close SQLite test template: " + err.Error())
	}

	exitCode := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(exitCode)
}

// newTestDB copies the migrated template database into a fresh temp file
// per test and opens it, guaranteeing each test starts from an
// already-migrated, empty database.
func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	copyMigratedTestDB(t, path)

	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func copyMigratedTestDB(t testing.TB, path string) {
	t.Helper()
	source, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatalf("open migrated SQLite test template: %v", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create SQLite test database: %v", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatalf("copy migrated SQLite test template: %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close copied SQLite test database: %v", err)
	}
}

func phraseProposal(phrase string, evidence ...string) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{
		Kind:          eventruleproposal.KindPhrase,
		TargetKind:    eventruleproposal.TargetAction,
		Target:        "signed",
		Phrase:        phrase,
		Description:   "d",
		EvidenceIDs:   evidence,
		Status:        eventruleproposal.StatusPending,
		PromptVersion: "v1",
		CreatedAt:     time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
}

func TestUpsert_DedupesAndMergesEvidence(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := sqliteeventruleproposal.NewRepository(db)

	first, err := repo.Upsert(ctx, phraseProposal("  Agrees To Terms ", "a1", "a2"))
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if !strings.HasPrefix(first.ID, "evr_") || len(first.ID) != 16 {
		t.Fatalf("ID = %q", first.ID)
	}
	if first.Phrase != "agrees to terms" {
		t.Fatalf("Phrase = %q, want trimmed+lowercased", first.Phrase)
	}
	second, err := repo.Upsert(ctx, phraseProposal("agrees to terms", "a2", "a3"))
	if err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("ID changed: %q vs %q", second.ID, first.ID)
	}
	if want := []string{"a1", "a2", "a3"}; !reflect.DeepEqual(second.EvidenceIDs, want) {
		t.Fatalf("evidence = %v, want %v", second.EvidenceIDs, want)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_rule_proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, err = %v", n, err)
	}
	got, err := repo.Get(ctx, first.ID)
	if err != nil || !reflect.DeepEqual(got.EvidenceIDs, second.EvidenceIDs) || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
}

func TestUpsert_TypeProposalKeyIgnoresCase(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	mk := func(name string) eventruleproposal.Proposal {
		return eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: name, Status: eventruleproposal.StatusPending, PromptVersion: "v1", CreatedAt: time.Now()}
	}
	a, err := repo.Upsert(ctx, mk(" Practice Squad "))
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Upsert(ctx, mk("practice squad"))
	if err != nil || a.ID != b.ID || a.TypeName != "practice squad" {
		t.Fatalf("a=%+v b=%+v err=%v", a, b, err)
	}
}

func TestUpsert_RejectedIsNotResurrected(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))

	p, err := repo.Upsert(ctx, phraseProposal("cut", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := p.Reject(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, rejected); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	again, err := repo.Upsert(ctx, phraseProposal("cut", "a9"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != eventruleproposal.StatusRejected || again.ID != p.ID || !reflect.DeepEqual(again.EvidenceIDs, []string{"a1"}) {
		t.Fatalf("again = %+v", again)
	}
}

func TestListByStatusAndSave(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))

	a, _ := repo.Upsert(ctx, phraseProposal("one", "e1"))
	b, _ := repo.Upsert(ctx, phraseProposal("two", "e2"))

	approved, err := a.Approve(time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, approved); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	pending, err := repo.ListByStatus(ctx, eventruleproposal.StatusPending)
	if err != nil || len(pending) != 1 || pending[0].ID != b.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	got, err := repo.ListByStatus(ctx, eventruleproposal.StatusApproved)
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("approved = %+v, %v", got, err)
	}
	if !got[0].DecidedAt.Equal(approved.DecidedAt) {
		t.Fatalf("DecidedAt = %v, want %v", got[0].DecidedAt, approved.DecidedAt)
	}
	if !pending[0].DecidedAt.IsZero() {
		t.Fatalf("pending DecidedAt = %v, want zero", pending[0].DecidedAt)
	}

	// Re-upserting an approved proposal returns it unchanged.
	same, err := repo.Upsert(ctx, phraseProposal("one", "zz"))
	if err != nil || same.Status != eventruleproposal.StatusApproved || !reflect.DeepEqual(same.EvidenceIDs, []string{"e1"}) {
		t.Fatalf("same = %+v, %v", same, err)
	}
}

func TestGetAndSave_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	if _, err := repo.Get(ctx, "evr_missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get() error = %v, want sql.ErrNoRows", err)
	}
	if err := repo.Save(ctx, eventruleproposal.Proposal{ID: "evr_missing", Status: eventruleproposal.StatusApproved}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Save() error = %v, want sql.ErrNoRows", err)
	}
}
