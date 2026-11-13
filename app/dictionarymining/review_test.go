package dictionarymining_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// memCandidates is an in-memory out.DictionaryCandidateRepository keyed by
// term, like the SQLite table.
type memCandidates struct {
	order []string
	byKey map[string]out.DictionaryCandidate
}

func (m *memCandidates) Save(_ context.Context, c out.DictionaryCandidate) error {
	if m.byKey == nil {
		m.byKey = map[string]out.DictionaryCandidate{}
	}
	if _, ok := m.byKey[c.Term]; ok {
		return nil
	}
	m.order = append(m.order, c.Term)
	m.byKey[c.Term] = c
	return nil
}

func (m *memCandidates) ListBySession(_ context.Context, sessionID string) ([]out.DictionaryCandidate, error) {
	var list []out.DictionaryCandidate
	for _, term := range m.order {
		if c := m.byKey[term]; c.SessionID == sessionID {
			list = append(list, c)
		}
	}
	return list, nil
}

func (m *memCandidates) ListRejectedTerms(context.Context) ([]string, error) { return nil, nil }

func (m *memCandidates) UpdateDecision(_ context.Context, term, status, category string, at time.Time) error {
	c := m.byKey[term]
	c.Status, c.CategoryGuess, c.DecidedAt = status, category, at
	m.byKey[term] = c
	return nil
}

type reviewEnv struct {
	deps    dictionarymining.ReviewDeps
	repo    *memCandidates
	dict    dictionary.Dictionary
	saves   int
	ignored []string
	ignErr  error
	out     bytes.Buffer
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	e := &reviewEnv{repo: &memCandidates{}}
	e.dict = dictionary.Dictionary{
		Version: "1.0.0", Name: "test", Language: "en",
		Decision: dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3},
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 12, Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Type: dictionary.EntryTypeOfficialName, Source: "manual", Confidence: 1}}},
			{Name: "coaching_staff", Weight: 4},
			{Name: "nfl_people_context", Weight: 2},
		},
	}
	e.deps = dictionarymining.ReviewDeps{
		Mine: func(_ context.Context, sessionID string) (dictionarymining.Selection, error) {
			return dictionarymining.Selection{
				Session: sessionID, Dictionary: e.dict,
				Filter:   dictionarymining.FilterConfig{MinSupport: 1},
				Warnings: []string{"WARNING: optional input missing"},
				Candidates: []dictionarymining.Candidate{
					{Term: "Ezo Quarrington", CategoryGuess: "nfl_people_context", SupportCount: 2, FirstSeenArticleID: "a1"},
					{Term: "camp", SupportCount: 1, FirstSeenArticleID: "a1"},
				},
			}, nil
		},
		Candidates:     e.repo,
		LoadDictionary: func() (dictionary.Dictionary, error) { return e.dict, nil },
		SaveDictionary: func(d dictionary.Dictionary) error { e.dict = d; e.saves++; return nil },
		IgnoreTerm: func(term string) (bool, error) {
			if e.ignErr != nil {
				return false, e.ignErr
			}
			e.ignored = append(e.ignored, term)
			return true, nil
		},
		PolicyPath: "policy.yml",
		Clock:      fixedClock{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		Out:        &e.out,
	}
	return e
}

func TestMiningPrepareSavesPendingAndOffersThem(t *testing.T) {
	e := newReviewEnv(t)
	p, err := dictionarymining.Prepare(context.Background(), e.deps, "s1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.Session != "s1" || p.MinedCount != 2 || len(p.Warnings) != 1 {
		t.Fatalf("prepared = %+v", p)
	}
	if want := []string{"coaching_staff", "nfl_people_context", "official_names"}; !reflect.DeepEqual(p.Categories, want) {
		t.Fatalf("categories = %v, want %v (sorted, as the CLI lists them)", p.Categories, want)
	}
	var terms []string
	for _, c := range p.Offered {
		terms = append(terms, c.Term)
	}
	if want := []string{"Ezo Quarrington", "camp"}; !reflect.DeepEqual(terms, want) {
		t.Fatalf("offered = %v, want %v", terms, want)
	}
	// A decided term is not offered again.
	if err := dictionarymining.Decide(context.Background(), e.deps, "s1", "camp", "reject", ""); err != nil {
		t.Fatal(err)
	}
	p, err = dictionarymining.Prepare(context.Background(), e.deps, "s1")
	if err != nil || len(p.Offered) != 1 || p.Offered[0].Term != "Ezo Quarrington" {
		t.Fatalf("offered after a decision = %+v (err %v)", p.Offered, err)
	}
}

func TestMiningDecideRejectAddsIgnoredTerm(t *testing.T) {
	e := newReviewEnv(t)
	if _, err := dictionarymining.Prepare(context.Background(), e.deps, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := dictionarymining.Decide(context.Background(), e.deps, "s1", "Ezo Quarrington", "reject", ""); err != nil {
		t.Fatalf("Decide reject: %v", err)
	}
	if got := e.repo.byKey["Ezo Quarrington"]; got.Status != "rejected" || got.CategoryGuess != "nfl_people_context" {
		t.Fatalf("candidate = %+v, want rejected keeping its guess", got)
	}
	if !reflect.DeepEqual(e.ignored, []string{"Ezo Quarrington"}) {
		t.Fatalf("ignored = %v", e.ignored)
	}
	if want := "  added \"ezo quarrington\" to ignored_terms in policy.yml\n"; e.out.String() != want {
		t.Fatalf("output = %q, want %q", e.out.String(), want)
	}

	e.out.Reset()
	e.ignErr = errors.New("no ignored_terms list")
	if err := dictionarymining.Decide(context.Background(), e.deps, "s1", "camp", "reject", ""); err != nil {
		t.Fatalf("a policy file failure is a warning, not an error: %v", err)
	}
	if want := "WARNING: rejected term not added to policy.yml: no ignored_terms list\n"; e.out.String() != want {
		t.Fatalf("output = %q, want %q", e.out.String(), want)
	}
}

func TestMiningDecideApproveAndCategorize(t *testing.T) {
	e := newReviewEnv(t)
	ctx := context.Background()
	if _, err := dictionarymining.Prepare(ctx, e.deps, "s1"); err != nil {
		t.Fatal(err)
	}
	// approve with no category takes a valid guess
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "Ezo Quarrington", "approve", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.repo.byKey["Ezo Quarrington"]; got.Status != "approved" || got.CategoryGuess != "nfl_people_context" {
		t.Fatalf("candidate = %+v", got)
	}
	// approve without a usable guess needs a category
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "camp", "approve", ""); err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("approve with no guess and no category = %v, want a category error", err)
	}
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "camp", "categorize", "not_a_category"); err == nil {
		t.Fatal("categorize into an unknown category must fail")
	}
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "camp", "categorize", "coaching_staff"); err != nil {
		t.Fatal(err)
	}
	if got := e.repo.byKey["camp"]; got.Status != "approved" || got.CategoryGuess != "coaching_staff" {
		t.Fatalf("candidate = %+v", got)
	}
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "camp", "skip", ""); err == nil {
		t.Fatal("an unknown action must fail")
	}
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "unknown term", "reject", ""); err == nil {
		t.Fatal("a term not mined for the session must fail")
	}
}

func TestMiningWriteBumpsOnce(t *testing.T) {
	e := newReviewEnv(t)
	ctx := context.Background()
	if _, err := dictionarymining.Prepare(ctx, e.deps, "s1"); err != nil {
		t.Fatal(err)
	}
	empty, err := dictionarymining.Write(ctx, e.deps, "s1")
	if err != nil || empty != (dictionarymining.WriteResult{}) || e.saves != 0 {
		t.Fatalf("Write with nothing approved = %+v, %v (saves %d)", empty, err, e.saves)
	}
	if err := dictionarymining.Decide(ctx, e.deps, "s1", "Ezo Quarrington", "approve", ""); err != nil {
		t.Fatal(err)
	}
	first, err := dictionarymining.Write(ctx, e.deps, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (dictionarymining.WriteResult{Approved: 1, Added: 1, FromVersion: "1.0.0", ToVersion: "1.0.1"}); first != want {
		t.Fatalf("first Write = %+v, want %+v", first, want)
	}
	cat := e.dict.Categories[2]
	if len(cat.Entries) != 1 || cat.Entries[0].CanonicalName != "Ezo Quarrington" || cat.Entries[0].Type != dictionary.EntryTypePerson || cat.Entries[0].Source != "dictionary_update" {
		t.Fatalf("entries = %+v", cat.Entries)
	}
	second, err := dictionarymining.Write(ctx, e.deps, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (dictionarymining.WriteResult{Approved: 1}); second != want || e.saves != 1 || e.dict.Version != "1.0.1" {
		t.Fatalf("second Write = %+v (saves %d, version %s), want nothing changed", second, e.saves, e.dict.Version)
	}
}
