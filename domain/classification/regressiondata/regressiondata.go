// Package regressiondata embeds a small, synthetic regression dataset of
// headline/body pairs (about the fictional "Springfield Isotopes") and their
// expected classification.Decide outcome. It exists so both
// classification's own seed test and the regressioncheck package (run from
// the CLI, from any working directory) can share one dataset instead of
// maintaining copies that drift apart. A host program whose dictionary
// covers a real team replaces it with its own reviewed cases through
// SetCases or SetCasesJSON.
package regressiondata

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

//go:embed seed_cases.json
var seedCasesFS embed.FS

// Case is one regression example: a title/body pair and whether it should
// be classified as related to the team the dictionary covers.
type Case struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	WantRelated bool   `json:"want_related"`
}

var override atomic.Pointer[[]Case]

// Cases returns the regression dataset in use: the cases set through
// SetCases/SetCasesJSON, or else the versioned dataset embedded at build
// time — usable from any working directory, including the CLI binary, not
// just from tests run inside this package's directory.
func Cases() ([]Case, error) {
	if p := override.Load(); p != nil {
		return append([]Case(nil), (*p)...), nil
	}
	return EmbeddedCases()
}

// EmbeddedCases returns the library's embedded synthetic dataset,
// regardless of any override.
func EmbeddedCases() ([]Case, error) {
	data, err := seedCasesFS.ReadFile("seed_cases.json")
	if err != nil {
		return nil, fmt.Errorf("regressiondata: read seed_cases.json: %w", err)
	}
	return parse(data)
}

// SetCases replaces the dataset Cases returns. A host program calls it once
// at startup; nil restores the embedded dataset.
func SetCases(cases []Case) {
	if cases == nil {
		override.Store(nil)
		return
	}
	c := append([]Case(nil), cases...)
	override.Store(&c)
}

// SetCasesJSON parses data (the seed_cases.json format: a JSON array of
// {name, title, body, want_related}) and installs it through SetCases.
func SetCasesJSON(data []byte) error {
	cases, err := parse(data)
	if err != nil {
		return err
	}
	SetCases(cases)
	return nil
}

func parse(data []byte) ([]Case, error) {
	var cases []Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("regressiondata: unmarshal: %w", err)
	}
	return cases, nil
}
