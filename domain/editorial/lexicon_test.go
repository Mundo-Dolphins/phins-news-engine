package editorial

import "testing"

func TestLexiconFeatures_Clickbait(t *testing.T) {
	fv := lexiconFeatures("You Won't Believe What Happened Next!!! This changes everything")
	if fv["lex:clickbait"] == 0 {
		t.Error("expected lex:clickbait > 0 for a sensationalist headline")
	}
}

func TestLexiconFeatures_Promo(t *testing.T) {
	fv := lexiconFeatures("This post is sponsored by Acme Corp. Shop now and save 20% off.")
	if fv["lex:promo"] == 0 {
		t.Error("expected lex:promo > 0 for sponsored/shop-now language")
	}
}

func TestLexiconFeatures_Rumor(t *testing.T) {
	fv := lexiconFeatures("Sources say the team could reportedly be exploring a trade")
	if fv["lex:rumor"] == 0 {
		t.Error("expected lex:rumor > 0 for 'sources say'/'reportedly'/'could'")
	}
}

func TestLexiconFeatures_Contractual(t *testing.T) {
	fv := lexiconFeatures("The team signed him to a contract extension after he was waived and re-signed")
	if fv["lex:contractual"] == 0 {
		t.Error("expected lex:contractual > 0 for contract/waived/signed language")
	}
}

func TestLexiconFeatures_VerifiableData(t *testing.T) {
	fv := lexiconFeatures("He agreed to a $12.5 million deal, a 25% raise over his prior contract")
	if fv["lex:verifiable_data"] == 0 {
		t.Error("expected lex:verifiable_data > 0 for dollar amounts and percentages")
	}
}

func TestLexiconFeatures_NeutralTextScoresZeroEverywhere(t *testing.T) {
	fv := lexiconFeatures("The team practiced on Wednesday ahead of Sunday's game")
	for k, v := range fv {
		if v != 0 {
			t.Errorf("feature %q = %v on neutral text, want 0", k, v)
		}
	}
}

func TestLexiconFeatures_CaseInsensitive(t *testing.T) {
	lower := lexiconFeatures("this is sponsored content")
	upper := lexiconFeatures("THIS IS SPONSORED CONTENT")
	if lower["lex:promo"] != upper["lex:promo"] {
		t.Errorf("case sensitivity mismatch: %v vs %v", lower["lex:promo"], upper["lex:promo"])
	}
}

// Regression test: "re-signed" should count as 1, not 2 (from "re-signed" and "signed" substrings).
func TestLexiconFeatures_ResignedNoDobleCount(t *testing.T) {
	fv := lexiconFeatures("The player was re-signed to a new deal")
	if fv["lex:contractual"] != 1.0 {
		t.Errorf("re-signed should count as 1 contractual signal, got %v", fv["lex:contractual"])
	}
}

// Regression test: "resigned" (quitting) should NOT trigger contractual,
// nor should other words containing "signed" as a substring.
func TestLexiconFeatures_SignedFalsePositives(t *testing.T) {
	tests := []struct {
		text     string
		desc     string
		wantZero bool
	}{
		{"The coach resigned after the season", "resigned (quitting)", true},
		{"He designed the new offensive scheme", "designed", true},
		{"The job was assigned to him", "assigned", true},
		{"The unsigned album from 2020", "unsigned", true},
		{"The goods were consigned to storage", "consigned", true},
	}
	for _, test := range tests {
		fv := lexiconFeatures(test.text)
		if test.wantZero && fv["lex:contractual"] != 0.0 {
			t.Errorf("%s: expected lex:contractual = 0, got %v", test.desc, fv["lex:contractual"])
		}
	}
}
