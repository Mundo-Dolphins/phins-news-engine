package configdoc

import (
	"strings"
	"testing"
)

const sample = `# top head comment
freshness:
  # freshness head comment
  social_hours: 3   # was 6 — on game days the team plays late
  social_max: 5
  game_window_hours: 24
scraping_service:
  enabled: true
  domains:
    e2news.com:
      request_timeout: 15
llm_servers:
  - name: primary
    base_url: https://example.com
`

func TestApplyPreservesComments(t *testing.T) {
	doc, err := DocFromBytes([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Apply([]Edit{{Path: []string{"freshness", "social_hours"}, Value: 6}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := doc.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"social_hours: 6",
		"# was 6 — on game days the team plays late",
		"# top head comment",
		"# freshness head comment",
		"game_window_hours: 24",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "social_hours: 3") {
		t.Errorf("value was not updated:\n%s", s)
	}
}

func TestApplyNestedMapAndAddKey(t *testing.T) {
	doc, err := DocFromBytes([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edits := []Edit{
		{Path: []string{"scraping_service", "domains", "e2news.com", "request_timeout"}, Value: 30},
		{Path: []string{"scraping_service", "domains", "example.org", "enabled"}, Value: true},
		{Path: []string{"scraping_service", "domains", "example.org", "request_timeout"}, Value: 10},
	}
	if err := doc.Apply(edits); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, _ := doc.Render()
	s := string(out)
	if !strings.Contains(s, "request_timeout: 30") {
		t.Errorf("nested scalar not updated:\n%s", s)
	}
	if !strings.Contains(s, "example.org:") {
		t.Errorf("added map key missing:\n%s", s)
	}
}

func TestApplyListElement(t *testing.T) {
	doc, err := DocFromBytes([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Apply([]Edit{{Path: []string{"llm_servers", "0", "base_url"}, Value: "https://new.example.com"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, _ := doc.Render()
	s := string(out)
	if !strings.Contains(s, "https://new.example.com") {
		t.Errorf("list element not updated:\n%s", s)
	}
	if !strings.Contains(s, "name: primary") {
		t.Errorf("sibling list field lost:\n%s", s)
	}
}

func TestApplyStringDurationStaysString(t *testing.T) {
	doc, err := DocFromBytes([]byte("cache_ttl: 720h\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Apply([]Edit{{Path: []string{"cache_ttl"}, Value: "96h"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, _ := doc.Render()
	s := string(out)
	if !strings.Contains(s, `cache_ttl: 96h`) {
		t.Errorf("duration string not preserved as a string:\n%s", s)
	}
}

func TestApplyRemoveKey(t *testing.T) {
	doc, err := DocFromBytes([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Apply([]Edit{{Path: []string{"freshness", "social_max"}, Remove: true}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, _ := doc.Render()
	if strings.Contains(string(out), "social_max") {
		t.Errorf("key not removed:\n%s", string(out))
	}
	if !strings.Contains(string(out), "game_window_hours: 24") {
		t.Errorf("sibling lost after removal:\n%s", string(out))
	}
}

// TestApplyArraySetAppendRemove covers editing an existing list element,
// appending, and removing an element.
func TestApplyArraySetAppendRemove(t *testing.T) {
	doc, err := DocFromBytes([]byte("items:\n  - a\n  - b\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Edit{
		{Path: []string{"items", "0"}, Value: "a-prime"},
		{Path: []string{"items", "2"}, Value: "c"},
		{Path: []string{"items", "1"}, Remove: true},
	} {
		if err := doc.Apply([]Edit{e}); err != nil {
			t.Fatalf("apply %v: %v", e, err)
		}
	}
	val, err := doc.Value()
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map[string]any", val)
	}
	items, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("items is %T, want []any", m["items"])
	}
	if len(items) != 2 || items[0] != "a-prime" || items[1] != "c" {
		t.Errorf("got %v, want [a-prime c]", items)
	}
}

func TestValueAndComments(t *testing.T) {
	doc, err := DocFromBytes([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	val, err := doc.Value()
	if err != nil {
		t.Fatalf("value: %v", err)
	}
	m, ok := val.(map[string]any)
	if !ok {
		t.Fatalf("value is not a map: %T", val)
	}
	fresh, ok := m["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("freshness missing: %v", m["freshness"])
	}
	if fresh["social_hours"] != 3 {
		t.Errorf("social_hours = %v, want 3", fresh["social_hours"])
	}
	comments := doc.Comments()
	if got := comments["freshness/social_hours"]; !strings.Contains(got, "was 6") {
		t.Errorf("comment for freshness/social_hours = %q, want it to mention the rationale", got)
	}
}
