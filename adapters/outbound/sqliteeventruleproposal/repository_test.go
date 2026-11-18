// adapters/outbound/sqliteeventruleproposal/repository_test.go

package sqliteeventruleproposal_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/Mundo-Dolphins/phins-news-engine/ports/eventruleproposalport"
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
	if err != nil || a.ID != b.ID || a.TypeName != "practice-squad" {
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

func TestTransition_StatusChangedSinceRead(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	p, err := repo.Upsert(ctx, phraseProposal("cut", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	approved, _ := p.Approve(at)
	if err := repo.Transition(ctx, approved, eventruleproposal.StatusPending); err != nil {
		t.Fatal(err)
	}
	// A second decision read while the row was pending: it is approved now.
	rejected, _ := p.Reject(at)
	if err := repo.Transition(ctx, rejected, eventruleproposal.StatusPending); !errors.Is(err, eventruleproposalport.ErrStatusChanged) {
		t.Fatalf("Transition() error = %v, want ErrStatusChanged", err)
	}
	got, err := repo.Get(ctx, p.ID)
	if err != nil || got.Status != eventruleproposal.StatusApproved {
		t.Fatalf("got %+v, %v; want it still approved", got, err)
	}
}

func TestTransition_NotFound(t *testing.T) {
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	err := repo.Transition(context.Background(), eventruleproposal.Proposal{ID: "evr_missing", Status: eventruleproposal.StatusApproved}, eventruleproposal.StatusPending)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Transition() error = %v, want sql.ErrNoRows", err)
	}
}

func typeProposal(name, target string, evidence ...string) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{
		Kind:          eventruleproposal.KindType,
		TypeName:      name,
		Target:        target,
		Description:   "d",
		EvidenceIDs:   evidence,
		Status:        eventruleproposal.StatusPending,
		PromptVersion: "v1",
		CreatedAt:     time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
}

func TestUpsert_TypeSlugIgnoresTarget(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))

	first, err := repo.Upsert(ctx, typeProposal("player injury status", "Injury-Reserve Move", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Upsert(ctx, typeProposal("player_injury_status", "", "a2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.TypeName != "player-injury-status" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if want := []string{"a1", "a2"}; !reflect.DeepEqual(second.EvidenceIDs, want) {
		t.Fatalf("evidence = %v, want %v", second.EvidenceIDs, want)
	}
	if second.Target != "Injury-Reserve Move" {
		t.Fatalf("Target = %q", second.Target)
	}
	pending, err := repo.ListByStatus(ctx, eventruleproposal.StatusPending)
	if err != nil || len(pending) != 1 || pending[0].Target != "Injury-Reserve Move" {
		t.Fatalf("pending = %+v, err = %v", pending, err)
	}
}

func TestUpsert_TypeFirstNonEmptyTarget(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))

	if _, err := repo.Upsert(ctx, typeProposal("contract-news", "", "a1")); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Upsert(ctx, typeProposal("Contract News", "Signing", "a2"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "Signing" {
		t.Fatalf("Target = %q, want Signing", got.Target)
	}
	stored, err := repo.Get(ctx, got.ID)
	if err != nil || stored.Target != "Signing" {
		t.Fatalf("stored = %+v, err = %v", stored, err)
	}
}

func TestUpsert_TypeDoesNotResurrectRejected(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))

	p, err := repo.Upsert(ctx, typeProposal("player injury status", "", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := p.Reject(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	again, err := repo.Upsert(ctx, typeProposal("Player Injury Status", "", "a9"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != eventruleproposal.StatusRejected || !reflect.DeepEqual(again.EvidenceIDs, []string{"a1"}) {
		t.Fatalf("again = %+v", again)
	}
}

func seedType(t *testing.T, repo *sqliteeventruleproposal.Repository, name string, status eventruleproposal.Status, evidence ...string) eventruleproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	p, err := repo.Upsert(ctx, typeProposal(name, "", evidence...))
	if err != nil {
		t.Fatal(err)
	}
	if status != eventruleproposal.StatusPending {
		p.Status = status
		if err := repo.Save(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestMergeTypes(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	src := seedType(t, repo, "kicking error", eventruleproposal.StatusPending, "a1")
	dst := seedType(t, repo, "kicker miss", eventruleproposal.StatusApproved, "a2")
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	got, err := repo.MergeTypes(ctx, src.ID, eventruleproposal.StatusPending, dst.ID, eventruleproposal.StatusApproved, now)
	if err != nil {
		t.Fatalf("MergeTypes() error = %v", err)
	}
	if want := []string{"a2", "a1"}; !reflect.DeepEqual(got.EvidenceIDs, want) || got.Status != eventruleproposal.StatusApproved {
		t.Fatalf("returned = %+v", got)
	}
	storedDst, err := repo.Get(ctx, dst.ID)
	if err != nil || !reflect.DeepEqual(storedDst.EvidenceIDs, []string{"a2", "a1"}) || storedDst.Status != eventruleproposal.StatusApproved {
		t.Fatalf("dst = %+v, err = %v", storedDst, err)
	}
	storedSrc, err := repo.Get(ctx, src.ID)
	if err != nil || storedSrc.Status != eventruleproposal.StatusRejected || !storedSrc.DecidedAt.Equal(now) {
		t.Fatalf("src = %+v, err = %v", storedSrc, err)
	}
}

func TestMergeTypes_StatusChangedWritesNothing(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	src := seedType(t, repo, "kicking error", eventruleproposal.StatusPending, "a1")
	dst := seedType(t, repo, "kicker miss", eventruleproposal.StatusApproved, "a2")

	_, err := repo.MergeTypes(ctx, src.ID, eventruleproposal.StatusPending, dst.ID, eventruleproposal.StatusPending, time.Now())
	if !errors.Is(err, eventruleproposalport.ErrStatusChanged) {
		t.Fatalf("err = %v, want ErrStatusChanged", err)
	}
	storedSrc, err := repo.Get(ctx, src.ID)
	if err != nil || storedSrc.Status != eventruleproposal.StatusPending || !storedSrc.DecidedAt.IsZero() {
		t.Fatalf("src = %+v, err = %v", storedSrc, err)
	}
	storedDst, err := repo.Get(ctx, dst.ID)
	if err != nil || !reflect.DeepEqual(storedDst.EvidenceIDs, []string{"a2"}) {
		t.Fatalf("dst = %+v, err = %v", storedDst, err)
	}
}

func TestReplaceTypes(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := sqliteeventruleproposal.NewRepository(db)
	a := seedType(t, repo, "kicking error", eventruleproposal.StatusPending, "a1")
	b := seedType(t, repo, "kicker miss", eventruleproposal.StatusPending, "a2")

	if err := repo.ReplaceTypes(ctx, []string{a.ID, b.ID}, []eventruleproposal.Proposal{typeProposal("Special Teams Error", "", "a1", "a2")}); err != nil {
		t.Fatalf("ReplaceTypes() error = %v", err)
	}
	pending, err := repo.ListByStatus(ctx, eventruleproposal.StatusPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, err = %v", pending, err)
	}
	if pending[0].TypeName != "special-teams-error" || !reflect.DeepEqual(pending[0].EvidenceIDs, []string{"a1", "a2"}) {
		t.Fatalf("row = %+v", pending[0])
	}
	// A later Upsert of the same slug must land on the replaced row.
	again, err := repo.Upsert(ctx, typeProposal("special teams error", "", "a3"))
	if err != nil || again.ID != pending[0].ID {
		t.Fatalf("again = %+v, err = %v (want ID %s)", again, err, pending[0].ID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_rule_proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, err = %v", n, err)
	}
}

func TestTypeApprovalColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	created := seedType(t, repo, "kicking error", eventruleproposal.StatusPending, "a1")
	if created.TypeWindow != "48h" || created.TypeReferential {
		t.Fatalf("fresh proposal = %+v, want the default approval choices", created)
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil || got.TypeWindow != "48h" || got.TypeReferential {
		t.Fatalf("stored defaults = %+v, %v; want 48h and false", got, err)
	}

	approved, err := got.ApproveType("72h", true, time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionType(ctx, approved, eventruleproposal.StatusPending); err != nil {
		t.Fatalf("TransitionType() error = %v", err)
	}
	got, err = repo.Get(ctx, created.ID)
	if err != nil || got.Status != eventruleproposal.StatusApproved || got.TypeWindow != "72h" || !got.TypeReferential {
		t.Fatalf("after approval = %+v, %v", got, err)
	}
	// The guard holds: a second decision from pending no longer lands.
	if err := repo.TransitionType(ctx, approved, eventruleproposal.StatusPending); !errors.Is(err, eventruleproposalport.ErrStatusChanged) {
		t.Fatalf("second TransitionType() error = %v, want ErrStatusChanged", err)
	}

	// ReplaceTypes writes both columns of an inserted proposal.
	ins := typeProposal("logo change", "", "a2")
	ins.Status, ins.TypeWindow, ins.TypeReferential = eventruleproposal.StatusApproved, "24h", true
	if err := repo.ReplaceTypes(ctx, nil, []eventruleproposal.Proposal{ins}); err != nil {
		t.Fatal(err)
	}
	approvedList, err := repo.ListByStatus(ctx, eventruleproposal.StatusApproved)
	if err != nil || len(approvedList) != 2 {
		t.Fatalf("approved = %+v, %v", approvedList, err)
	}
	var logo eventruleproposal.Proposal
	for _, p := range approvedList {
		if p.TypeName == "logo-change" {
			logo = p
		}
	}
	if logo.TypeWindow != "24h" || !logo.TypeReferential {
		t.Fatalf("replaced row = %+v, want 24h and referential", logo)
	}
}

func TestRetargetPhrases_RekeysWithTheDerivedIDAndMergesIntoAnExistingKey(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	decided := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	kick := phraseProposal("misses a kick", "a1")
	kick.Target = "kicking-error"
	kick, err := repo.Upsert(ctx, kick)
	if err != nil {
		t.Fatal(err)
	}
	kick.Status, kick.DecidedAt = eventruleproposal.StatusApproved, decided
	if err := repo.Save(ctx, kick); err != nil {
		t.Fatal(err)
	}
	shank := phraseProposal("shanks the kick", "a2")
	shank.Target = "kicking-error"
	if _, err := repo.Upsert(ctx, shank); err != nil {
		t.Fatal(err)
	}
	existing := phraseProposal("shanks the kick", "a3")
	existing.Target = "field-goal-miss"
	existing, err = repo.Upsert(ctx, existing)
	if err != nil {
		t.Fatal(err)
	}
	other := phraseProposal("inks a deal", "a4")
	other, err = repo.Upsert(ctx, other)
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.RetargetPhrases(ctx, "kicking-error", "field-goal-miss")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("RetargetPhrases() = %+v", got)
	}
	byPhrase := map[string]eventruleproposal.Proposal{}
	for _, p := range got {
		byPhrase[p.Phrase] = p
	}
	moved := byPhrase["misses a kick"]
	if moved.Target != "field-goal-miss" || moved.Status != eventruleproposal.StatusApproved || !moved.DecidedAt.Equal(decided) ||
		!reflect.DeepEqual(moved.EvidenceIDs, []string{"a1"}) || moved.ID == kick.ID || moved.PromptVersion != "v1" {
		t.Errorf("moved = %+v", moved)
	}
	if stored, err := repo.Get(ctx, moved.ID); err != nil || !reflect.DeepEqual(stored, moved) {
		t.Errorf("stored = %+v, %v; want %+v", stored, err, moved)
	}
	// Re-keying keeps the derived ID: a fresh Upsert of the new key finds it.
	again := phraseProposal("misses a kick")
	again.Target = "field-goal-miss"
	if up, err := repo.Upsert(ctx, again); err != nil || up.ID != moved.ID {
		t.Errorf("Upsert of the new key = %+v, %v; want id %s", up, err, moved.ID)
	}
	merged := byPhrase["shanks the kick"]
	if merged.ID != existing.ID || !reflect.DeepEqual(merged.EvidenceIDs, []string{"a3", "a2"}) {
		t.Errorf("merged = %+v", merged)
	}
	if _, err := repo.Get(ctx, kick.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old key still stored: %v", err)
	}
	if got, err := repo.Get(ctx, other.ID); err != nil || got.Target != "signed" {
		t.Errorf("unrelated phrase = %+v, %v", got, err)
	}
}

func TestRejectPhrasesFor(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(newTestDB(t))
	now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	var ids []string
	for i, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved, eventruleproposal.StatusApplied} {
		p := phraseProposal(fmt.Sprintf("misses kick %d", i), "a1")
		p.Target = "kicking-error"
		p, err := repo.Upsert(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		p.Status = st
		if err := repo.Save(ctx, p); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	other, err := repo.Upsert(ctx, phraseProposal("inks a deal", "a2"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := repo.RejectPhrasesFor(ctx, "kicking-error", now)
	if err != nil || n != 2 {
		t.Fatalf("RejectPhrasesFor() = %d, %v; want 2", n, err)
	}
	for i, want := range []eventruleproposal.Status{eventruleproposal.StatusRejected, eventruleproposal.StatusRejected, eventruleproposal.StatusApplied} {
		got, err := repo.Get(ctx, ids[i])
		if err != nil || got.Status != want {
			t.Errorf("proposal %d = %+v, %v; want %s", i, got, err, want)
		}
		if want == eventruleproposal.StatusRejected && !got.DecidedAt.Equal(now) {
			t.Errorf("proposal %d decided at %v", i, got.DecidedAt)
		}
	}
	if got, _ := repo.Get(ctx, other.ID); got.Status != eventruleproposal.StatusPending {
		t.Errorf("unrelated phrase is %s", got.Status)
	}
}
