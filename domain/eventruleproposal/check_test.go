// domain/eventruleproposal/check_test.go
package eventruleproposal

import "testing"

func TestCheck_RejectsRetypedStopwordShortAndTooBroad(t *testing.T) {
	TeamStopwords = []string{"Springfield", "Isotopes"}
	t.Cleanup(func() { TeamStopwords = nil })
	opt := DefaultCheckOptions()
	good := ImpactResult{Matches: 2, DistinctClusters: 2}
	phrase := func(s string) Proposal { return Proposal{Kind: KindPhrase, Phrase: s} }
	tests := []struct {
		name   string
		p      Proposal
		impact ImpactResult
		total  int
		ok     bool
	}{
		{"good", phrase("inks a deal"), good, 100, true},
		{"single distinctive word", phrase("tryout"), good, 100, false}, // 6 chars but <8
		{"single long word", phrase("restructure"), good, 100, true},
		{"single long team stopword", phrase("isotopes"), good, 100, false},
		{"retyped", phrase("inks a deal"), ImpactResult{Matches: 2, DistinctClusters: 2, Retyped: 1}, 100, false},
		{"short", phrase("a b"), good, 100, false},
		{"every token a stopword", phrase("the team"), good, 100, false},
		{"stopwords plus a real word", phrase("the team inks"), good, 100, true},
		{"six tokens", phrase("signs a one year deal today"), good, 100, true},
		{"longer than six tokens", phrase("signs a one year deal with them"), good, 100, false},
		{"already in the vocabulary", phrase("inks a deal"), ImpactResult{Matches: 2, DistinctClusters: 2, InVocabulary: "action_terms.sign"}, 100, false},
		{"too broad", phrase("inks a deal"), ImpactResult{Matches: 60, DistinctClusters: 5}, 100, false},
		{"broad but few clusters", phrase("inks a deal"), ImpactResult{Matches: 60, DistinctClusters: 2}, 100, true},
		{"no matches", phrase("inks a deal"), ImpactResult{}, 100, false},
		{"type skips impact", Proposal{Kind: KindType, TypeName: "x"}, ImpactResult{Retyped: 3}, 100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(tc.p, tc.impact, tc.total, opt)
			if v.OK != tc.ok {
				t.Fatalf("OK=%v reasons=%v", v.OK, v.Reasons)
			}
			if !v.OK && len(v.Reasons) == 0 {
				t.Fatal("no reasons")
			}
		})
	}
}

func TestCheck_NoMatchesUsesExportedReason(t *testing.T) {
	v := Check(Proposal{Kind: KindPhrase, Phrase: "inks a deal"}, ImpactResult{}, 100, DefaultCheckOptions())
	if v.OK || len(v.Reasons) != 1 || v.Reasons[0] != ReasonNoMatches {
		t.Fatalf("%+v", v)
	}
}

func TestCheck_ZeroMaxTokensMeansNoLimit(t *testing.T) {
	opt := DefaultCheckOptions()
	if opt.MaxTokens != 6 {
		t.Fatalf("default MaxTokens = %d, want 6", opt.MaxTokens)
	}
	opt.MaxTokens = 0
	v := Check(Proposal{Kind: KindPhrase, Phrase: "signs a one year deal with them"}, ImpactResult{Matches: 1, DistinctClusters: 1}, 100, opt)
	if !v.OK {
		t.Fatalf("%+v", v)
	}
}

func TestDefaultCheckOptions_TeamStopwordsAreEmptyByDefault(t *testing.T) {
	if len(TeamStopwords) != 0 {
		t.Fatalf("TeamStopwords = %v, want none by default", TeamStopwords)
	}
	opt := DefaultCheckOptions()
	if len(opt.Stopwords) != len(DefaultStopwords) {
		t.Fatalf("stopwords = %v, want exactly DefaultStopwords", opt.Stopwords)
	}
	v := Check(Proposal{Kind: KindPhrase, Phrase: "isotopes"}, ImpactResult{Matches: 2, DistinctClusters: 2}, 100, opt)
	if !v.OK {
		t.Fatalf("without team stopwords a long single word is accepted, got %+v", v)
	}
}
