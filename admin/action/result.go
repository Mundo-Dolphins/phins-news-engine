package action

// Result is the renderable outcome of an action.
type Result struct {
	// Render is "table", "object" or "text".
	Render  string           `json:"render"`
	Columns []Column         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
	Object  any              `json:"object,omitempty"`
	Text    string           `json:"text,omitempty"`
	Summary string           `json:"summary,omitempty"`
}

// Column describes one table column of a [Result].
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
