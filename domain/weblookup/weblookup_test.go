package weblookup_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

func TestBuildTemplateQuery(t *testing.T) {
	team := weblookup.TeamNames{Full: "Springfield Isotopes", Short: "Isotopes"}
	tests := []struct {
		name string
		kind weblookup.QueryKind
		who  string
		team weblookup.TeamNames
		want string
	}{
		{"unknown player", weblookup.QueryUnknownPlayer, "Jai'Rene' Mossbank", team, `"Jai'Rene' Mossbank" Isotopes`},
		{"position verification", weblookup.QueryPositionVerification, "Jai'Rene' Mossbank", team, `"Jai'Rene' Mossbank" NFL position`},
		{"roster relation", weblookup.QueryTeamRosterRelation, "Jai'Rene' Mossbank", team, `"Jai'Rene' Mossbank" Springfield Isotopes`},
		{"coach or executive", weblookup.QueryCoachOrExecutive, "Jeff Harrowby", team, `"Jeff Harrowby" Isotopes coach`},
		{"no team falls back to the league", weblookup.QueryUnknownPlayer, "Jordan Sample", weblookup.TeamNames{}, `"Jordan Sample" NFL`},
		{"no team roster relation", weblookup.QueryTeamRosterRelation, "Jordan Sample", weblookup.TeamNames{}, `"Jordan Sample" NFL`},
		{"no team coach", weblookup.QueryCoachOrExecutive, "Jordan Sample", weblookup.TeamNames{}, `"Jordan Sample" NFL coach`},
		{"full name only", weblookup.QueryCoachOrExecutive, "Jordan Sample", weblookup.TeamNames{Full: "Springfield Isotopes"}, `"Jordan Sample" Springfield Isotopes coach`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := weblookup.BuildTemplateQuery(tt.kind, tt.who, tt.team)
			if got != tt.want {
				t.Fatalf("BuildTemplateQuery(%v, %q, %+v) = %q, want %q", tt.kind, tt.who, tt.team, got, tt.want)
			}
		})
	}
}

func TestValidateQuery(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"trims whitespace", "  Jai'Rene' Mossbank Isotopes  ", "Jai'Rene' Mossbank Isotopes", false},
		{"rejects empty", "   ", "", true},
		{"rejects too long", strings.Repeat("a", 201), "", true},
		{"accepts max length", strings.Repeat("a", 200), strings.Repeat("a", 200), false},
		{"rejects disallowed characters", "Mossbank <script>", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := weblookup.ValidateQuery(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateQuery(%q) error = nil, want an error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateQuery(%q) error = %v, want nil", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateQuery(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestIsTrustedDomain_ExactMatch(t *testing.T) {
	if !weblookup.IsTrustedDomain("league.example.com", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = false, want true for an exact match")
	}
}

func TestIsTrustedDomain_SubdomainMatch(t *testing.T) {
	if !weblookup.IsTrustedDomain("www.league.example.com", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = false, want true for a real subdomain")
	}
}

func TestIsTrustedDomain_RejectsLookalikeDomain(t *testing.T) {
	if weblookup.IsTrustedDomain("league.example.com.attacker.test", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = true, want false for a lookalike domain (league.example.com.attacker.test)")
	}
}

func TestIsTrustedDomain_RejectsUnrelatedDomain(t *testing.T) {
	if weblookup.IsTrustedDomain("example.com", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = true, want false for an unrelated domain")
	}
}

func TestIsTrustedDomain_NormalizesCaseAndTrailingDot(t *testing.T) {
	if !weblookup.IsTrustedDomain("WWW.LEAGUE.EXAMPLE.COM.", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = false, want true after case/trailing-dot normalization")
	}
}

func TestIsTrustedDomain_EmptyHostRejected(t *testing.T) {
	if weblookup.IsTrustedDomain("", []string{"league.example.com"}) {
		t.Fatal("IsTrustedDomain() = true, want false for an empty host")
	}
}

func TestIsTrustedDomain_NoTrustedDomainsConfigured(t *testing.T) {
	if weblookup.IsTrustedDomain("league.example.com", nil) {
		t.Fatal("IsTrustedDomain() = true, want false when no domain is trusted")
	}
}

func TestParseDisposition(t *testing.T) {
	tests := []struct {
		in     string
		want   weblookup.Disposition
		wantOK bool
	}{
		{"keep", weblookup.DispositionKeep, true},
		{"  REJECT ", weblookup.DispositionReject, true},
		{"Uncertain", weblookup.DispositionUncertain, true},
		{"banana", weblookup.DispositionUncertain, false},
		{"", weblookup.DispositionUncertain, false},
	}
	for _, tt := range tests {
		got, ok := weblookup.ParseDisposition(tt.in)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("ParseDisposition(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}
