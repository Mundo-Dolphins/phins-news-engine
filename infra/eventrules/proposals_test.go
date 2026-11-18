// infra/eventrules/proposals_test.go
package eventrules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
)

// knownNames is the reference taxonomy's known event type and action names.
func knownNames() []string {
	return eventrulecycle.KnownTypeNames(eventsignalstest.Taxonomy())
}

func typeProposal(name string, status eventruleproposal.Status, evidence ...string) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: name, Description: name + ".", EvidenceIDs: evidence, Status: status, PromptVersion: "v1"}
}

func phraseProposal() eventruleproposal.Proposal {
	return eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: "sign", Phrase: "inks a deal", Status: eventruleproposal.StatusPending, PromptVersion: "v1"}
}

func TestListTypesPendingThenApprovedTypesOnly(t *testing.T) {
	db := openTestDB(t)
	seedProposals(t, db,
		typeProposal("approved one", eventruleproposal.StatusApproved),
		typeProposal("pending one", eventruleproposal.StatusPending),
		phraseProposal(),
		typeProposal("rejected one", eventruleproposal.StatusRejected),
	)
	got, err := ListTypes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TypeName != "pending-one" || got[1].TypeName != "approved-one" {
		t.Fatalf("ListTypes() = %+v, want pending then approved types only", got)
	}
}

func TestDecideTypeApprovesAndRejects(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("a", eventruleproposal.StatusPending), typeProposal("b", eventruleproposal.StatusPending))
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	approved, err := DecideType(context.Background(), db, ids[0], true, TypeApproval{}, knownNames(), now)
	if err != nil || approved.Status != eventruleproposal.StatusApproved {
		t.Fatalf("approve: %+v, %v", approved, err)
	}
	rejected, err := DecideType(context.Background(), db, ids[1], false, TypeApproval{}, knownNames(), now)
	if err != nil || rejected.Status != eventruleproposal.StatusRejected {
		t.Fatalf("reject: %+v, %v", rejected, err)
	}
	got, err := ListTypes(context.Background(), db)
	if err != nil || len(got) != 1 || got[0].ID != ids[0] {
		t.Fatalf("ListTypes() = %+v, %v; want only the approved one", got, err)
	}
}

func TestDecideTypeRejectsPhraseProposal(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, phraseProposal())
	_, err := DecideType(context.Background(), db, ids[0], true, TypeApproval{}, knownNames(), time.Now())
	if !errors.Is(err, ErrNotTypeProposal) {
		t.Fatalf("err = %v, want ErrNotTypeProposal", err)
	}
	if want := "is a phrase proposal, not a type proposal; phrases are decided in `events review`"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to contain %q", err, want)
	}
	got, _ := ListTypes(context.Background(), db)
	if len(got) != 0 {
		t.Fatalf("a phrase proposal must stay untouched, ListTypes() = %+v", got)
	}
}

func TestDecideTypeAlreadyDecided(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("old", eventruleproposal.StatusRejected))
	_, err := DecideType(context.Background(), db, ids[0], true, TypeApproval{}, knownNames(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "cannot move") {
		t.Fatalf("err = %v, want the domain error", err)
	}
	if errors.Is(err, ErrNotTypeProposal) {
		t.Fatal("a decided type proposal is not a phrase proposal")
	}
}

func TestDecideTypeUnknownIDIsNotFound(t *testing.T) {
	db := openTestDB(t)
	_, err := DecideType(context.Background(), db, "evr_nope", true, TypeApproval{}, knownNames(), time.Now())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want errors.Is sql.ErrNoRows", err)
	}
}

// typePhrase is a phrase proposal targeting the action of the type name.
func typePhrase(name, phrase string, status eventruleproposal.Status, evidence ...string) eventruleproposal.Proposal {
	return eventruleproposal.Proposal{Kind: eventruleproposal.KindPhrase, TargetKind: eventruleproposal.TargetAction, Target: name, Phrase: phrase, EvidenceIDs: evidence, Status: status, PromptVersion: "v1"}
}

func statusOf(t *testing.T, db *sql.DB, id string) eventruleproposal.Status {
	t.Helper()
	p, err := sqliteeventruleproposal.NewRepository(db).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func TestDecideTypeRejectRejectsItsPhrases(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking-error", eventruleproposal.StatusApproved),
		typePhrase("kicking-error", "misses a kick", eventruleproposal.StatusPending, "a1"),
		typePhrase("kicking-error", "shanks the kick", eventruleproposal.StatusApproved, "a2"),
		typePhrase("sign", "inks a deal", eventruleproposal.StatusPending, "a3"),
	)
	if _, err := DecideType(context.Background(), db, ids[0], false, TypeApproval{}, knownNames(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for i, want := range []eventruleproposal.Status{eventruleproposal.StatusRejected, eventruleproposal.StatusRejected, eventruleproposal.StatusRejected, eventruleproposal.StatusPending} {
		if got := statusOf(t, db, ids[i]); got != want {
			t.Errorf("proposal %d is %s, want %s", i, got, want)
		}
	}
}

func TestMergeType_ApprovedIntoProposalRetargetsPhrases(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking-error", eventruleproposal.StatusApproved, "a1"),
		typeProposal("field-goal-miss", eventruleproposal.StatusPending, "a2"),
		typePhrase("kicking-error", "misses a kick", eventruleproposal.StatusPending, "a1"),
	)
	res, err := MergeType(context.Background(), db, ids[0], ids[1], knownNames(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Retargeted) != 1 || res.Retargeted[0].Target != "field-goal-miss" || res.Retargeted[0].Phrase != "misses a kick" || res.Retargeted[0].Status != eventruleproposal.StatusPending {
		t.Fatalf("Retargeted = %+v", res.Retargeted)
	}
	if _, err := sqliteeventruleproposal.NewRepository(db).Get(context.Background(), ids[2]); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old phrase still stored: %v", err)
	}
	if got := statusOf(t, db, res.Retargeted[0].ID); got != eventruleproposal.StatusPending {
		t.Errorf("retargeted phrase is %s", got)
	}
}

func TestMergeType_IntoKnownTypeRejectsPhrases(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking-error", eventruleproposal.StatusApproved, "a1"),
		typePhrase("kicking-error", "misses a kick", eventruleproposal.StatusPending, "a1"),
	)
	if _, err := MergeType(context.Background(), db, ids[0], "game", knownNames(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, db, ids[1]); got != eventruleproposal.StatusRejected {
		t.Errorf("phrase is %s, want rejected", got)
	}
}

func TestMergeType_IntoProposal(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking error", eventruleproposal.StatusPending, "a1", "a2"),
		typeProposal("missed kick", eventruleproposal.StatusApproved, "a2", "a3"),
	)
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	res, err := MergeType(context.Background(), db, ids[0], ids[1], knownNames(), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.ID != ids[0] || res.Source.Status != eventruleproposal.StatusRejected || !res.Source.DecidedAt.Equal(now) {
		t.Errorf("Source = %+v, want %s rejected at now", res.Source, ids[0])
	}
	if res.CoveredBy != "" || res.Target.ID != ids[1] || res.Target.Status != eventruleproposal.StatusApproved {
		t.Errorf("Target = %+v, CoveredBy = %q; want %s approved", res.Target, res.CoveredBy, ids[1])
	}
	if want := []string{"a2", "a3", "a1"}; strings.Join(res.Target.EvidenceIDs, ",") != strings.Join(want, ",") {
		t.Errorf("Target evidence = %v, want %v", res.Target.EvidenceIDs, want)
	}
	got, err := ListTypes(context.Background(), db)
	if err != nil || len(got) != 1 || got[0].ID != ids[1] || len(got[0].EvidenceIDs) != 3 {
		t.Fatalf("ListTypes() = %+v, %v; want only the merged target", got, err)
	}
}

// A concurrent decision between the read and the merge surfaces as
// ErrAlreadyDecided with the "decided elsewhere" text. The trigger makes the
// guarded update of the source touch no row, as a lost race does.
func TestMergeType_DecidedElsewhere(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking error", eventruleproposal.StatusPending, "a1"),
		typeProposal("missed kick", eventruleproposal.StatusPending, "a2"),
	)
	if _, err := db.Exec(`CREATE TRIGGER lost_race BEFORE UPDATE ON event_rule_proposals
		WHEN OLD.id = '` + ids[0] + `' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	_, err := MergeType(context.Background(), db, ids[0], ids[1], knownNames(), time.Now())
	if !errors.Is(err, ErrAlreadyDecided) || !strings.Contains(err.Error(), "decided elsewhere") {
		t.Fatalf("err = %v, want ErrAlreadyDecided with decided elsewhere", err)
	}
}

func TestMergeType_IntoKnownType(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("contract extension", eventruleproposal.StatusApproved, "a1"))
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	res, err := MergeType(context.Background(), db, ids[0], " Extend ", knownNames(), now)
	if err != nil {
		t.Fatal(err)
	}
	if res.CoveredBy != "extend" || res.Target.ID != "" {
		t.Errorf("CoveredBy = %q, Target = %+v; want extend and no target", res.CoveredBy, res.Target)
	}
	if res.Source.Status != eventruleproposal.StatusRejected || !res.Source.DecidedAt.Equal(now) {
		t.Errorf("Source = %+v, want rejected at now", res.Source)
	}
	if got, err := ListTypes(context.Background(), db); err != nil || len(got) != 0 {
		t.Fatalf("ListTypes() = %+v, %v; want none left", got, err)
	}
}

func TestMergeType_IntoAKnownNameOfTheGivenList(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("delta move", eventruleproposal.StatusPending, "a1"), typeProposal("other", eventruleproposal.StatusPending, "a2"))
	res, err := MergeType(context.Background(), db, ids[0], "delta", []string{"delta"}, time.Now())
	if err != nil || res.CoveredBy != "delta" {
		t.Fatalf("MergeType(into delta) = %+v, %v; want covered by delta", res, err)
	}
	if _, err := MergeType(context.Background(), db, ids[1], "game", []string{"delta"}, time.Now()); !errors.Is(err, ErrBadMergeTarget) {
		t.Fatalf("err = %v, want ErrBadMergeTarget for a name outside the list", err)
	}
}

func TestMergeType_Refused(t *testing.T) {
	cases := []struct {
		name string
		into func(ids []string) string
		src  eventruleproposal.Status
		want error
	}{
		{"into itself", func(ids []string) string { return ids[0] }, eventruleproposal.StatusPending, ErrBadMergeTarget},
		{"into a rejected proposal", func(ids []string) string { return ids[2] }, eventruleproposal.StatusPending, ErrBadMergeTarget},
		{"into a phrase proposal", func(ids []string) string { return ids[3] }, eventruleproposal.StatusPending, ErrBadMergeTarget},
		{"into an unknown name", func([]string) string { return "no-such-kind" }, eventruleproposal.StatusPending, ErrBadMergeTarget},
		{"source already rejected", func(ids []string) string { return ids[1] }, eventruleproposal.StatusRejected, ErrAlreadyDecided},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			ids := seedProposals(t, db,
				typeProposal("kicking error", tc.src, "a1"),
				typeProposal("missed kick", eventruleproposal.StatusPending, "a2"),
				typeProposal("logo change", eventruleproposal.StatusRejected, "a3"),
				phraseProposal(),
			)
			repo := sqliteeventruleproposal.NewRepository(db)
			before := map[string]eventruleproposal.Proposal{}
			for _, id := range ids {
				p, err := repo.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				before[id] = p
			}
			_, err := MergeType(context.Background(), db, ids[0], tc.into(ids), knownNames(), time.Now())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			for _, id := range ids {
				p, err := repo.Get(context.Background(), id)
				if err != nil || !reflect.DeepEqual(p, before[id]) {
					t.Errorf("proposal %s changed: %+v, want %+v (%v)", id, p, before[id], err)
				}
			}
		})
	}
}

// insertRawTypeProposal stores a type proposal row as is, bypassing the
// slug keying of Upsert, as rows written before it look.
func insertRawTypeProposal(t *testing.T, db *sql.DB, id, name string, status eventruleproposal.Status, target string, created time.Time, evidence ...string) {
	t.Helper()
	blob, err := json.Marshal(append([]string{}, evidence...))
	if err != nil {
		t.Fatal(err)
	}
	var decided any
	if status != eventruleproposal.StatusPending {
		decided = created.Add(time.Minute).Format(time.RFC3339Nano)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO event_rule_proposals (id, kind, target_kind, target, phrase, type_name, description,
			evidence_json, status, prompt_version, created_at, decided_at) VALUES (?, 'type', '', ?, '', ?, ?, ?, ?, 'v2', ?, ?)`,
		id, target, name, name+".", string(blob), string(status), created.Format(time.RFC3339Nano), decided); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeTypes(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		name   string
		status eventruleproposal.Status
		target string
		ev     []string
	}{
		{"player injury status", eventruleproposal.StatusApproved, "Injury-Reserve Move", []string{"e1"}},
		{"Player Injury Status", eventruleproposal.StatusApproved, "", []string{"e2"}},
		{"player_injury_status", eventruleproposal.StatusApproved, "", []string{"e3"}},
		{"Game", eventruleproposal.StatusApproved, "", []string{"e4"}},
		{"kicking error", eventruleproposal.StatusPending, "", []string{"e5"}},
		{"logo change", eventruleproposal.StatusRejected, "", nil},
		{"///", eventruleproposal.StatusPending, "", nil},
	}
	for i, r := range rows {
		insertRawTypeProposal(t, db, fmt.Sprintf("evr_raw%d", i), r.name, r.status, r.target, base.Add(time.Duration(i)*time.Hour), r.ev...)
	}
	ids := seedProposals(t, db, phraseProposal())
	now := base.Add(48 * time.Hour)

	rep, err := NormalizeTypes(context.Background(), db, true, knownNames(), now)
	if err != nil {
		t.Fatal(err)
	}
	want := NormalizeReport{Before: 7, After: 4, Merged: 1, Covered: 1, Reopened: 1, Emptied: 1}
	if rep != want {
		t.Fatalf("report = %+v, want %+v", rep, want)
	}
	got, err := ListTypes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.TypeName+":"+string(p.Status))
	}
	if strings.Join(names, ",") != "player-injury-status:pending,kicking-error:pending" {
		t.Fatalf("ListTypes() = %v, want the pending survivors", names)
	}
	if strings.Join(got[0].EvidenceIDs, ",") != "e1,e2,e3" || got[0].Target != "Injury-Reserve Move" {
		t.Errorf("player-injury-status = %+v, want evidence e1,e2,e3 and the earliest target", got[0])
	}
	if _, err := sqliteeventruleproposal.NewRepository(db).Get(context.Background(), ids[0]); err != nil {
		t.Errorf("the phrase proposal must survive: %v", err)
	}

	again, err := NormalizeTypes(context.Background(), db, true, knownNames(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if want := (NormalizeReport{Before: 4, After: 4}); again != want {
		t.Fatalf("second run = %+v, want %+v", again, want)
	}
}

func TestDecideTypePersistsApprovalOptions(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("a", eventruleproposal.StatusPending),
		typeProposal("b", eventruleproposal.StatusPending),
		typeProposal("c", eventruleproposal.StatusPending),
		typeProposal("d", eventruleproposal.StatusPending))
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()
	repo := sqliteeventruleproposal.NewRepository(db)

	if _, err := DecideType(ctx, db, ids[0], true, TypeApproval{Window: "72h", Referential: true}, knownNames(), now); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, ids[0])
	if err != nil || got.TypeWindow != "72h" || !got.TypeReferential {
		t.Fatalf("stored = %+v, %v; want 72h and referential", got, err)
	}
	if _, err := DecideType(ctx, db, ids[1], true, TypeApproval{}, knownNames(), now); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.Get(ctx, ids[1])
	if got.TypeWindow != "48h" || got.TypeReferential {
		t.Fatalf("defaults = %+v, want 48h and not referential", got)
	}
	// A bad window is refused and leaves the proposal pending.
	_, err = DecideType(ctx, db, ids[2], true, TypeApproval{Window: "0s"}, knownNames(), now)
	if !errors.Is(err, ErrBadTypeApproval) {
		t.Fatalf("err = %v, want ErrBadTypeApproval", err)
	}
	got, _ = repo.Get(ctx, ids[2])
	if got.Status != eventruleproposal.StatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	// Options are ignored when rejecting, even a bad window.
	rejected, err := DecideType(ctx, db, ids[3], false, TypeApproval{Window: "abc", Referential: true}, knownNames(), now)
	if err != nil || rejected.Status != eventruleproposal.StatusRejected {
		t.Fatalf("reject = %+v, %v", rejected, err)
	}
	got, _ = repo.Get(ctx, ids[3])
	if got.TypeWindow != "48h" || got.TypeReferential {
		t.Fatalf("rejected row = %+v, want untouched approval choices", got)
	}
}

func TestNormalizeTypesSkipsAppliedSlug(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	insertRawTypeProposal(t, db, "evr_applied", "kicking-error", eventruleproposal.StatusApplied, "", base)
	insertRawTypeProposal(t, db, "evr_pending", "kicking_error", eventruleproposal.StatusPending, "", base.Add(time.Hour), "e1")

	rep, err := NormalizeTypes(context.Background(), db, false, knownNames(), base.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if want := (NormalizeReport{Before: 1, After: 0, Covered: 1}); rep != want {
		t.Fatalf("report = %+v, want %+v", rep, want)
	}
	repo := sqliteeventruleproposal.NewRepository(db)
	if _, err := repo.Get(context.Background(), "evr_pending"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("pending duplicate err = %v, want it deleted", err)
	}
	applied, err := repo.Get(context.Background(), "evr_applied")
	if err != nil || applied.Status != eventruleproposal.StatusApplied {
		t.Fatalf("applied row = %+v, %v; want it untouched", applied, err)
	}
}

func TestDecideTypeAndMergeKeepThePhrasesOfADeclaredAction(t *testing.T) {
	// A type proposal named like a declared action (added by hand since):
	// rejecting or merging it away must not reject that action's phrases.
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("sign", eventruleproposal.StatusApproved),
		typeProposal("trade", eventruleproposal.StatusPending),
		phraseProposal(),
		typePhrase("trade", "ships off", eventruleproposal.StatusApproved, "a1"),
	)
	if _, err := DecideType(context.Background(), db, ids[0], false, TypeApproval{}, knownNames(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeType(context.Background(), db, ids[1], "game", knownNames(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 3} {
		if got, want := statusOf(t, db, ids[i]), []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved}[i-2]; got != want {
			t.Errorf("phrase %d is %s, want %s (untouched)", i, got, want)
		}
	}
}

func TestAddTypePhrase(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db,
		typeProposal("kicking error", eventruleproposal.StatusApproved),
		typeProposal("betting odds", eventruleproposal.StatusPending),
		phraseProposal(),
	)
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	p, err := AddTypePhrase(ctx, db, ids[0], "  Misses A Kick ", now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != eventruleproposal.KindPhrase || p.TargetKind != eventruleproposal.TargetAction || p.Target != "kicking-error" ||
		p.Phrase != "misses a kick" || p.PromptVersion != "manual" || len(p.EvidenceIDs) != 0 || p.Status != eventruleproposal.StatusPending {
		t.Fatalf("AddTypePhrase() = %+v", p)
	}
	if got, err := sqliteeventruleproposal.NewRepository(db).Get(ctx, p.ID); err != nil || got.Status != eventruleproposal.StatusPending || got.Target != "kicking-error" {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	counts, err := TypePhraseCounts(ctx, db)
	if err != nil || counts["kicking-error"] != 1 {
		t.Fatalf("TypePhraseCounts() = %v, %v", counts, err)
	}

	if _, err := AddTypePhrase(ctx, db, ids[1], "posts the line", now); !errors.Is(err, ErrTypeNotApproved) {
		t.Errorf("pending type: err = %v, want ErrTypeNotApproved", err)
	}
	if _, err := AddTypePhrase(ctx, db, ids[2], "posts the line", now); !errors.Is(err, ErrNotTypeProposal) {
		t.Errorf("phrase proposal: err = %v, want ErrNotTypeProposal", err)
	}
	if _, err := AddTypePhrase(ctx, db, ids[0], "   ", now); !errors.Is(err, ErrEmptyPhrase) {
		t.Errorf("blank phrase: err = %v, want ErrEmptyPhrase", err)
	}
	if _, err := AddTypePhrase(ctx, db, "evr_nope", "x y", now); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown id: err = %v, want sql.ErrNoRows", err)
	}
	// The same phrase rejected earlier is not silently brought back.
	rejected, err := p.Reject(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteeventruleproposal.NewRepository(db).Transition(ctx, rejected, eventruleproposal.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, err := AddTypePhrase(ctx, db, ids[0], "misses a kick", now); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("rejected phrase: err = %v, want ErrAlreadyDecided", err)
	}
}

func TestTypePhraseCountsPendingAndApprovedActionPhrases(t *testing.T) {
	db := openTestDB(t)
	seedProposals(t, db,
		typePhrase("kicking-error", "misses a kick", eventruleproposal.StatusPending),
		typePhrase("kicking-error", "shanks the kick", eventruleproposal.StatusApproved),
		typePhrase("kicking-error", "hooks it wide", eventruleproposal.StatusRejected),
		typePhrase("kicking-error", "doinks it", eventruleproposal.StatusApplied),
		typeProposal("kicking-error", eventruleproposal.StatusApproved),
	)
	counts, err := TypePhraseCounts(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, map[string]int{"kicking-error": 2}) {
		t.Errorf("counts = %v, want kicking-error: 2", counts)
	}
}
