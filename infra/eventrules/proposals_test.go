// infra/eventrules/proposals_test.go
package eventrules

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

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
	if len(got) != 2 || got[0].TypeName != "pending one" || got[1].TypeName != "approved one" {
		t.Fatalf("ListTypes() = %+v, want pending then approved types only", got)
	}
}

func TestDecideTypeApprovesAndRejects(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("a", eventruleproposal.StatusPending), typeProposal("b", eventruleproposal.StatusPending))
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	approved, err := DecideType(context.Background(), db, ids[0], true, now)
	if err != nil || approved.Status != eventruleproposal.StatusApproved {
		t.Fatalf("approve: %+v, %v", approved, err)
	}
	rejected, err := DecideType(context.Background(), db, ids[1], false, now)
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
	_, err := DecideType(context.Background(), db, ids[0], true, time.Now())
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
	_, err := DecideType(context.Background(), db, ids[0], true, time.Now())
	if err == nil || !strings.Contains(err.Error(), "cannot move") {
		t.Fatalf("err = %v, want the domain error", err)
	}
	if errors.Is(err, ErrNotTypeProposal) {
		t.Fatal("a decided type proposal is not a phrase proposal")
	}
}

func TestDecideTypeUnknownIDIsNotFound(t *testing.T) {
	db := openTestDB(t)
	_, err := DecideType(context.Background(), db, "evr_nope", true, time.Now())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want errors.Is sql.ErrNoRows", err)
	}
	if _, err := Brief(context.Background(), db, "evr_nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Brief err = %v, want errors.Is sql.ErrNoRows", err)
	}
}

func TestBriefQuotesEvidenceAndSkipsPruned(t *testing.T) {
	db := openTestDB(t)
	seedArticles(t, db, "g", eventsignals.EventTypeGeneric, "Isotopes extend Ezo", "Dale gets new deal")
	ids := seedProposals(t, db, typeProposal("contract extension", eventruleproposal.StatusPending, "g0", "g1", "missing"))
	got, err := Brief(context.Background(), db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"contract extension", "- Isotopes extend Ezo", "- Dale gets new deal"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
}

func TestBriefPhraseProposal(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, phraseProposal())
	_, err := Brief(context.Background(), db, ids[0])
	if !errors.Is(err, ErrNotTypeProposal) || strings.Contains(err.Error(), "events review") {
		t.Fatalf("err = %v, want ErrNotTypeProposal with the short brief wording", err)
	}
}

func TestBriefRejectedProposal(t *testing.T) {
	db := openTestDB(t)
	ids := seedProposals(t, db, typeProposal("x type", eventruleproposal.StatusRejected))
	_, err := Brief(context.Background(), db, ids[0])
	if !errors.Is(err, ErrNoBrief) || !strings.Contains(err.Error(), "is rejected; only pending or approved type proposals have a brief") {
		t.Fatalf("err = %v, want ErrNoBrief", err)
	}
}
