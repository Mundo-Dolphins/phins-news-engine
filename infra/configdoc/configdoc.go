// Package configdoc edits YAML documents while preserving comments.
//
// It parses a file into a gopkg.in/yaml.v3 node tree (which carries each
// node's head/line/foot comments), applies surgical leaf edits addressed by
// key paths, and re-encodes the tree. An edit only touches the nodes on its
// path and carries the edited leaf's comment over, so every untouched line,
// key order, and comment survives the round trip. Callers validate the result
// (for example with config.Load) before writing it to disk.
package configdoc

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a parsed YAML document preserving comments.
type Doc struct {
	Node *yaml.Node // root document node
	Raw  []byte     // original bytes (for diff and backup)
}

// Edit is a single change: set the value at Path to Value, or delete it when
// Remove is true. Path is a sequence of mapping keys, with a trailing integer
// segment naming a sequence element, for example
// ["scraping_service", "domains", "e2news.com", "request_timeout"] or
// ["llm_servers", "0", "base_url"].
type Edit struct {
	Path   []string `json:"path"`
	Value  any      `json:"value,omitempty"`
	Remove bool     `json:"remove,omitempty"`
}

// LoadDoc reads and parses path into a Doc, preserving comments.
func LoadDoc(path string) (*Doc, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-provided config path
	if err != nil {
		return nil, fmt.Errorf("configdoc: read %s: %w", path, err)
	}
	return DocFromBytes(raw)
}

// DocFromBytes parses raw YAML into a Doc. The decode is lenient (unknown
// keys allowed); callers re-validate strictly before writing.
func DocFromBytes(raw []byte) (*Doc, error) {
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(false)
	if err := dec.Decode(&node); err != nil {
		return nil, fmt.Errorf("configdoc: parse yaml: %w", err)
	}
	return &Doc{Node: &node, Raw: raw}, nil
}

// Root returns the node under the document wrapper (usually a mapping).
func (d *Doc) Root() *yaml.Node {
	if d.Node == nil {
		return nil
	}
	if d.Node.Kind == yaml.DocumentNode && len(d.Node.Content) > 0 {
		return d.Node.Content[0]
	}
	return d.Node
}

// Value decodes the document root into a plain value: map[string]any for
// mappings, []any for sequences, and the scalar for leaves. Untyped scalars
// such as durations come back as their file text (e.g. "720h"), so the UI
// edits exactly what the operator sees.
func (d *Doc) Value() (any, error) {
	root := d.Root()
	if root == nil {
		return nil, nil
	}
	var out any
	if err := root.Decode(&out); err != nil {
		return nil, fmt.Errorf("configdoc: decode value: %w", err)
	}
	return out, nil
}

// Comments returns the comment for each mapping key, addressed by its key
// path joined with "/". It prefers the key's own line comment, then its head
// comment, then the value's line comment. The leading "#" and surrounding
// whitespace are stripped so the UI can render the text directly.
func (d *Doc) Comments() map[string]string {
	out := map[string]string{}
	root := d.Root()
	if root == nil {
		return out
	}
	var walk func(n *yaml.Node, prefix []string)
	walk = func(n *yaml.Node, prefix []string) {
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			path := append(append([]string{}, prefix...), key.Value)
			text := firstNonEmpty(strings.TrimSpace(val.LineComment),
				strings.TrimSpace(key.LineComment),
				strings.TrimSpace(key.HeadComment))
			if text != "" {
				out[strings.Join(path, "/")] = strings.TrimLeft(text, "# \t")
			}
			if val.Kind == yaml.MappingNode {
				walk(val, path)
			}
		}
	}
	walk(root, nil)
	return out
}

// Apply applies edits in place, preserving the comments of untouched nodes
// and carrying each edited leaf's comment onto its replacement.
func (d *Doc) Apply(edits []Edit) error {
	root := d.Root()
	if root == nil {
		return fmt.Errorf("configdoc: empty document")
	}
	for _, e := range edits {
		if len(e.Path) == 0 {
			return fmt.Errorf("configdoc: edit path must not be empty")
		}
		if err := applyEdit(root, e.Path, e); err != nil {
			return err
		}
	}
	return nil
}

// Render encodes the document back to YAML bytes with 2-space indentation.
func (d *Doc) Render() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.Node); err != nil {
		enc.Close()
		return nil, fmt.Errorf("configdoc: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("configdoc: encode: %w", err)
	}
	return buf.Bytes(), nil
}

func applyEdit(root *yaml.Node, path []string, e Edit) error {
	create := !e.Remove
	parent := root
	for _, seg := range path[:len(path)-1] {
		next, err := stepTo(parent, seg, create)
		if err != nil {
			return err
		}
		if next == nil {
			return fmt.Errorf("configdoc: path %v: segment %q not found", path, seg)
		}
		parent = next
	}
	last := path[len(path)-1]
	if e.Remove {
		return removeKey(parent, last)
	}
	return setKey(parent, last, e.Value)
}

// stepTo resolves one path segment: a mapping key or a sequence index. When
// create is set (a SET edit) a missing mapping key becomes a new mapping, so
// adding a new key under not-yet-present parents works; sequences are never
// extended here (only existing indices resolve).
func stepTo(n *yaml.Node, seg string, create bool) (*yaml.Node, error) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				return n.Content[i+1], nil
			}
		}
		if !create {
			return nil, nil
		}
		child := &yaml.Node{Kind: yaml.MappingNode}
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: seg}, child)
		return child, nil
	case yaml.SequenceNode:
		idx, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("configdoc: %q is not a sequence index", seg)
		}
		if idx >= 0 && idx < len(n.Content) {
			return n.Content[idx], nil
		}
		return nil, fmt.Errorf("configdoc: sequence index %d is out of range (length %d)", idx, len(n.Content))
	default:
		return nil, fmt.Errorf("configdoc: cannot step into a non-container node")
	}
}

// setKey sets the value at key under parent (a mapping key or sequence
// index), creating the pair when absent and carrying the old leaf's comment.
func setKey(parent *yaml.Node, key string, val any) error {
	node, err := nodeFromValue(val)
	if err != nil {
		return err
	}
	switch parent.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i].Value == key {
				old := parent.Content[i+1]
				carryComments(node, old)
				parent.Content[i+1] = node
				return nil
			}
		}
		parent.Content = append(parent.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			node)
		return nil
	case yaml.SequenceNode:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx > len(parent.Content) {
			return fmt.Errorf("configdoc: sequence index %q is invalid", key)
		}
		if idx == len(parent.Content) {
			parent.Content = append(parent.Content, node)
			return nil
		}
		carryComments(node, parent.Content[idx])
		parent.Content[idx] = node
		return nil
	default:
		return fmt.Errorf("configdoc: cannot set %q under a non-container node", key)
	}
}

// removeKey deletes the mapping key named key under parent, or the sequence
// element at index key.
func removeKey(parent *yaml.Node, key string) error {
	switch parent.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(parent.Content); i += 2 {
			if parent.Content[i].Value == key {
				parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
				return nil
			}
		}
		return fmt.Errorf("configdoc: key %q not found", key)
	case yaml.SequenceNode:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(parent.Content) {
			return fmt.Errorf("configdoc: sequence index %q is invalid for removal", key)
		}
		parent.Content = append(parent.Content[:idx], parent.Content[idx+1:]...)
		return nil
	default:
		return fmt.Errorf("configdoc: cannot remove %q from a non-container node", key)
	}
}

// nodeFromValue builds a yaml.Node from a Go value (scalar or nested).
// Marshalling through yaml.v3 lets it pick the correct tag for numbers,
// booleans, and strings that look like other types.
func nodeFromValue(val any) (*yaml.Node, error) {
	if val == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	b, err := yaml.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("configdoc: marshal value: %w", err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("configdoc: parse value: %w", err)
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
		}
		return n.Content[0], nil
	}
	return &n, nil
}

// carryComments copies the old leaf's comments onto the replacement node so a
// changed value keeps its inline rationale.
func carryComments(dst, src *yaml.Node) {
	dst.HeadComment = src.HeadComment
	dst.LineComment = src.LineComment
	dst.FootComment = src.FootComment
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
