package action

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strconv"
)

// Decode turns raw JSON into a pointer to a fresh copy of the action's
// params struct, applying defaults and checking required, enum, types and
// unknown fields. Empty or null raw is treated as {}. Failures are *Error
// with CodeInvalidParams naming the offending field. Actions without Params
// decode to nil.
func Decode(a Action, raw json.RawMessage) (any, error) {
	fields, err := paramFields(a.Params)
	if err != nil {
		return nil, err
	}
	if a.Params == nil {
		return nil, nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		var ute *json.UnmarshalTypeError
		if asTypeErr(err, &ute) {
			return nil, invalidParams("params must be a JSON object")
		}
		return nil, invalidParams("params are not valid JSON: %v", err)
	}
	known := map[string]bool{}
	for _, f := range fields {
		known[f.name] = true
	}
	for k := range obj {
		if !known[k] {
			return nil, invalidParams("unknown field %q", k)
		}
	}
	t := reflect.TypeOf(a.Params)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	ptr := reflect.New(t)
	for _, f := range fields {
		dst := ptr.Elem().Field(f.index)
		v, present := obj[f.name]
		if present && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			present = false
		}
		switch {
		case present:
			if err := setField(dst, f, v); err != nil {
				return nil, err
			}
		case f.def != nil:
			dst.Set(reflect.ValueOf(f.def).Convert(f.typ))
		case f.required:
			return nil, invalidParams("field %q is required", f.name)
		}
		if f.enum != nil && f.typ.Kind() == reflect.String {
			if s := dst.String(); (present || f.def != nil || f.required) && !slices.Contains(f.enum, s) {
				return nil, invalidParams("field %q must be one of %v, got %q", f.name, f.enum, s)
			}
		}
	}
	return ptr.Interface(), nil
}

func asTypeErr(err error, target **json.UnmarshalTypeError) bool {
	ute, ok := err.(*json.UnmarshalTypeError)
	if ok {
		*target = ute
	}
	return ok
}

func setField(dst reflect.Value, f field, raw json.RawMessage) error {
	bad := func(want string) error { return invalidParams("field %q must be %s", f.name, want) }
	switch f.typ.Kind() {
	case reflect.Int, reflect.Int64:
		n, ok := parseInteger(raw)
		if !ok {
			return bad("an integer")
		}
		dst.SetInt(n)
	default:
		if err := json.Unmarshal(raw, dst.Addr().Interface()); err != nil {
			return bad("of type " + jsonType(f.typ))
		}
	}
	return nil
}

func parseInteger(raw json.RawMessage) (int64, bool) {
	s := string(bytes.TrimSpace(raw))
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	// Accept integral spellings such as 5.0 or 1e3, never fractions.
	if fl, err := strconv.ParseFloat(s, 64); err == nil && fl == math.Trunc(fl) && math.Abs(fl) < 1<<53 {
		return int64(fl), true
	}
	return 0, false
}
