// infra/nflverse/roster_test.go
package nflverse_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
)

func TestParseRoster_KeepsLatestWeekPerPlayer(t *testing.T) {
	f, err := os.Open("testdata/roster_sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	players, err := nflverse.ParseRoster(f)
	if err != nil {
		t.Fatalf("ParseRoster() error = %v", err)
	}
	if len(players) != 5 {
		t.Fatalf("len = %d, want 5 (Avery Placeholder's week-4 row must be dropped)", len(players))
	}
	for _, p := range players {
		if p.FullName == "Avery Placeholder" && p.Week != 5 {
			t.Fatalf("Avery Placeholder week = %d, want 5", p.Week)
		}
	}
}

func TestParseRoster_MissingRequiredColumnIsAnError(t *testing.T) {
	_, err := nflverse.ParseRoster(strings.NewReader("full_name,team\nA B,SPR\n"))
	if err == nil {
		t.Fatal("ParseRoster() error = nil, want error for missing columns")
	}
}

func TestParseRoster_EmptyInputIsAnError(t *testing.T) {
	if _, err := nflverse.ParseRoster(strings.NewReader("")); err == nil {
		t.Fatal("ParseRoster() error = nil, want error for empty input")
	}
}

func TestParseRoster_ReadsOptionalNameColumns(t *testing.T) {
	csvData := "full_name,first_name,last_name,football_name,team,position,status,gsis_id,week\n" +
		"Adair Mossgrove,Adair,Mossgrove,Adair,LKV,WR,ACT,g1,5\n"
	players, err := nflverse.ParseRoster(strings.NewReader(csvData))
	if err != nil || len(players) != 1 {
		t.Fatalf("ParseRoster() = %v, %v", players, err)
	}
	p := players[0]
	if p.FirstName != "Adair" || p.LastName != "Mossgrove" || p.FootballName != "Adair" {
		t.Fatalf("name columns = %q %q %q", p.FirstName, p.LastName, p.FootballName)
	}
}

func TestParseRoster_NameColumnsAreOptional(t *testing.T) {
	players, err := nflverse.ParseRoster(strings.NewReader("full_name,team,position,status,gsis_id,week\nA B,SPR,WR,ACT,g,1\n"))
	if err != nil || len(players) != 1 || players[0].LastName != "" {
		t.Fatalf("ParseRoster() = %v, %v; want one player with empty optional names", players, err)
	}
}
