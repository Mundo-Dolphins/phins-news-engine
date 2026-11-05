package action

import (
	"reflect"
	"testing"
)

type sampleParams struct {
	Status string   `json:"status" help:"Claim status" enum:"open,done" required:"true"`
	Limit  int      `json:"limit" help:"Max rows" default:"1"`
	Ratio  float64  `json:"ratio" default:"0.5"`
	Force  bool     `json:"force" default:"true"`
	Tags   []string `json:"tags"`
	Since  string   `json:"since" format:"duration"`
	Skip   string   `json:"-"`
	hidden string
}

func TestSchemaFromTags(t *testing.T) {
	_ = sampleParams{}.hidden
	got := Schema(sampleParams{})
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "description": "Claim status", "enum": []string{"open", "done"}},
			"limit":  map[string]any{"type": "integer", "description": "Max rows", "default": 1},
			"ratio":  map[string]any{"type": "number", "default": 0.5},
			"force":  map[string]any{"type": "boolean", "default": true},
			"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"since":  map[string]any{"type": "string", "format": "duration"},
		},
		"required":             []string{"status"},
		"additionalProperties": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema =\n%#v\nwant\n%#v", got, want)
	}
}

func TestSchemaNilParams(t *testing.T) {
	got := Schema(nil)
	want := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema = %#v", got)
	}
}
