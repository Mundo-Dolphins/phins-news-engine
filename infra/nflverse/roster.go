// infra/nflverse/roster.go
package nflverse

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Player is one row of the nflverse roster file that the verifier needs.
type Player struct {
	FullName string
	Team     string // nflverse team abbreviation, e.g. "WAV", "LA"
	Position string
	Status   string // ACT, DEV, RES, CUT, RET, INA, EXE, ...
	GSISID   string
	ESPNID   string
	PFRID    string
	Week     int
	// FirstName, LastName and FootballName come from optional columns and are
	// empty when the file lacks them.
	FirstName    string
	LastName     string
	FootballName string
}

// DefaultURL is the nflverse-data release asset for a season's roster.
func DefaultURL(season int) string {
	return fmt.Sprintf("https://github.com/nflverse/nflverse-data/releases/download/rosters/roster_%d.csv", season)
}

var requiredColumns = []string{"full_name", "team", "position", "status", "gsis_id", "week"}

// ParseRoster reads a nflverse roster CSV selecting columns by name. When a
// player appears in several weeks it keeps only the highest week. Rows
// without a full_name are skipped.
func ParseRoster(r io.Reader) ([]Player, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("nflverse: empty roster file")
		}
		return nil, fmt.Errorf("nflverse: read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	for _, name := range requiredColumns {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("nflverse: roster file missing column %q", name)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	latest := map[string]Player{}
	var order []string
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("nflverse: read row: %w", err)
		}
		name := get(rec, "full_name")
		if name == "" {
			continue
		}
		week, _ := strconv.Atoi(get(rec, "week"))
		p := Player{
			FullName: name, Team: get(rec, "team"), Position: get(rec, "position"), Status: get(rec, "status"),
			GSISID: get(rec, "gsis_id"), ESPNID: get(rec, "espn_id"), PFRID: get(rec, "pfr_id"), Week: week,
			FirstName: get(rec, "first_name"), LastName: get(rec, "last_name"), FootballName: get(rec, "football_name"),
		}
		key := p.GSISID
		if key == "" {
			key = p.FullName + "|" + p.Team
		}
		prev, seen := latest[key]
		if !seen {
			order = append(order, key)
		}
		if !seen || p.Week >= prev.Week {
			latest[key] = p
		}
	}
	out := make([]Player, 0, len(order))
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out, nil
}
