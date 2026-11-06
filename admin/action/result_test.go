package action

import (
	"encoding/json"
	"testing"
)

func TestResultColumnJSON(t *testing.T) {
	b, err := json.Marshal(Result{Render: "table", Columns: []Column{{Key: "id", Label: "ID"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"render":"table","columns":[{"key":"id","label":"ID"}]}`; string(b) != want {
		t.Fatalf("got %s", b)
	}
}
