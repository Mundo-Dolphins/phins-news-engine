package regressiondata_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification/regressiondata"
)

func TestCases_ReturnsSixteenSyntheticSeedCases(t *testing.T) {
	cases, err := regressiondata.Cases()
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(cases) != 16 {
		t.Fatalf("len(Cases()) = %d, want 16", len(cases))
	}

	seenNames := make(map[string]bool, len(cases))
	for _, c := range cases {
		if c.Name == "" {
			t.Errorf("case has empty Name: %+v", c)
		}
		if seenNames[c.Name] {
			t.Errorf("duplicate case name %q", c.Name)
		}
		seenNames[c.Name] = true
		if c.Title == "" {
			t.Errorf("case %q has empty Title", c.Name)
		}
	}

	// Spot-check a couple of cases known from the original inline table to
	// confirm the JSON round-trips the expected fields, not just any 12
	// entries.
	want := map[string]bool{
		"official team name is strong override":                true,
		"ambiguous team term alone is the animal, not related": false,
	}
	found := map[string]bool{}
	for _, c := range cases {
		if wantRelated, ok := want[c.Name]; ok {
			found[c.Name] = true
			if c.WantRelated != wantRelated {
				t.Errorf("case %q WantRelated = %v, want %v", c.Name, c.WantRelated, wantRelated)
			}
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("expected case %q not found in Cases()", name)
		}
	}
}

func TestSetCases_OverridesAndRestoresTheEmbeddedDataset(t *testing.T) {
	t.Cleanup(func() { regressiondata.SetCases(nil) })
	if err := regressiondata.SetCasesJSON([]byte(`[{"name":"host case","title":"Host headline","body":"","want_related":true}]`)); err != nil {
		t.Fatalf("SetCasesJSON: %v", err)
	}
	cases, err := regressiondata.Cases()
	if err != nil || len(cases) != 1 || cases[0].Name != "host case" || !cases[0].WantRelated {
		t.Fatalf("Cases() after override = %+v, %v", cases, err)
	}
	if err := regressiondata.SetCasesJSON([]byte(`not json`)); err == nil {
		t.Fatal("SetCasesJSON accepted malformed JSON")
	}
	regressiondata.SetCases(nil)
	cases, err = regressiondata.Cases()
	if err != nil || len(cases) != 16 {
		t.Fatalf("Cases() after reset = %d cases, %v; want the 16 embedded ones", len(cases), err)
	}
}
