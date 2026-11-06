package action

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// field is the parsed form of one exported params struct field.
type field struct {
	index    int
	name     string
	typ      reflect.Type
	help     string
	format   string
	enum     []string
	required bool
	def      any // typed default, nil when none
}

func paramFields(params any) ([]field, error) {
	if params == nil {
		return nil, nil
	}
	t := reflect.TypeOf(params)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("params must be a struct, got %s", t)
	}
	var out []field
	seen := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate param %q", name)
		}
		seen[name] = true
		f := field{index: i, name: name, typ: sf.Type, help: sf.Tag.Get("help"), format: sf.Tag.Get("format"), required: sf.Tag.Get("required") == "true"}
		if jt := jsonType(sf.Type); jt == "" {
			return nil, fmt.Errorf("param %q: unsupported type %s", name, sf.Type)
		}
		if e := sf.Tag.Get("enum"); e != "" {
			if sf.Type.Kind() != reflect.String {
				return nil, fmt.Errorf("param %q: enum is only supported on string fields", name)
			}
			f.enum = strings.Split(e, ",")
		}
		if d, ok := sf.Tag.Lookup("default"); ok {
			v, err := parseDefault(sf.Type, d)
			if err != nil {
				return nil, fmt.Errorf("param %q: bad default %q: %w", name, d, err)
			}
			if ds, ok := v.(string); ok && f.enum != nil && !slices.Contains(f.enum, ds) {
				return nil, fmt.Errorf("param %q: default %q is not in enum %v", name, ds, f.enum)
			}
			f.def = v
		}
		out = append(out, f)
	}
	return out, nil
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return "array"
		}
	}
	return ""
}

func parseDefault(t reflect.Type, s string) (any, error) {
	switch t.Kind() {
	case reflect.String:
		return s, nil
	case reflect.Bool:
		return strconv.ParseBool(s)
	case reflect.Int:
		n, err := strconv.ParseInt(s, 10, 0)
		return int(n), err
	case reflect.Int64:
		return strconv.ParseInt(s, 10, 64)
	case reflect.Float64:
		return strconv.ParseFloat(s, 64)
	default: // []string
		return strings.Split(s, ","), nil
	}
}

// Schema returns the JSON Schema object describing params, derived from its
// struct tags. It panics when params is not a supported struct; Registry.Add
// reports the same condition earlier.
func Schema(params any) map[string]any {
	fields, err := paramFields(params)
	if err != nil {
		panic("action: " + err.Error())
	}
	props := map[string]any{}
	required := []string{}
	for _, f := range fields {
		p := map[string]any{"type": jsonType(f.typ)}
		if p["type"] == "array" {
			p["items"] = map[string]any{"type": "string"}
		}
		if f.help != "" {
			p["description"] = f.help
		}
		if f.def != nil {
			p["default"] = f.def
		}
		if f.enum != nil {
			p["enum"] = f.enum
		}
		if f.format != "" {
			p["format"] = f.format
		}
		props[f.name] = p
		if f.required {
			required = append(required, f.name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
