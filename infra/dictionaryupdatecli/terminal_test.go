package dictionaryupdatecli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryupdatecli"
)

func TestTerminal_Accept(t *testing.T) {
	in := strings.NewReader("a\n")
	var out bytes.Buffer
	term := dictionaryupdatecli.New(in, &out)

	action, edited, err := term.Next(context.Background(), dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "a player", Detail: "new"}, dictionaryupdate.ReviewState{Index: 0, Total: 1})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if action != dictionaryupdate.ActionAccept || edited != nil {
		t.Fatalf("got action=%v edited=%v", action, edited)
	}
	if !strings.Contains(out.String(), "a player") {
		t.Errorf("expected the proposal's canonical key rendered, got:\n%s", out.String())
	}
}

func TestTerminal_UnrecognizedInputReprompts(t *testing.T) {
	in := strings.NewReader("nonsense\nr\n")
	var out bytes.Buffer
	term := dictionaryupdatecli.New(in, &out)

	action, _, err := term.Next(context.Background(), dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "x"}, dictionaryupdate.ReviewState{Index: 0, Total: 1})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if action != dictionaryupdate.ActionReject {
		t.Fatalf("expected the loop to reprompt past unrecognized input and land on reject, got %v", action)
	}
	if !strings.Contains(out.String(), "unrecognized") {
		t.Errorf("expected an 'unrecognized' message, got:\n%s", out.String())
	}
}

func TestTerminal_Edit_ChangesCategoryAndAppendsNote(t *testing.T) {
	in := strings.NewReader("e\ncoaching_staff\nmoved by hand\n")
	var out bytes.Buffer
	term := dictionaryupdatecli.New(in, &out)

	action, edited, err := term.Next(context.Background(), dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "x", Category: "active_roster_core", Detail: "original"}, dictionaryupdate.ReviewState{Index: 0, Total: 1})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if action != dictionaryupdate.ActionEdit || edited == nil {
		t.Fatalf("got action=%v edited=%v", action, edited)
	}
	if edited.Category != "coaching_staff" {
		t.Errorf("Category = %q, want coaching_staff", edited.Category)
	}
	if !strings.Contains(edited.Detail, "moved by hand") {
		t.Errorf("Detail = %q, expected it to contain the appended note", edited.Detail)
	}
}

func TestTerminal_InputClosedReturnsAbortAndError(t *testing.T) {
	in := strings.NewReader("") // EOF immediately
	var out bytes.Buffer
	term := dictionaryupdatecli.New(in, &out)

	action, _, err := term.Next(context.Background(), dictionaryupdate.Proposal{Kind: dictionaryupdate.ProposalAddEntity, CanonicalKey: "x"}, dictionaryupdate.ReviewState{Index: 0, Total: 1})
	if err == nil {
		t.Fatal("expected an error when input is closed")
	}
	if action != dictionaryupdate.ActionAbort {
		t.Errorf("action = %v, want ActionAbort", action)
	}
}
