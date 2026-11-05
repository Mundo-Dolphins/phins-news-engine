package action

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type decParams struct {
	Status string   `json:"status" enum:"open,done" default:"open"`
	Limit  int      `json:"limit" default:"1"`
	Name   string   `json:"name" required:"true"`
	Tags   []string `json:"tags" default:"a,b"`
	On     bool     `json:"on"`
}

func decAction() Action { return Action{Name: "x", Params: decParams{}} }

func invalid(t *testing.T, err error, field string) {
	t.Helper()
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeInvalidParams {
		t.Fatalf("err = %v, want invalid_params", err)
	}
	if !strings.Contains(ae.Message, field) {
		t.Fatalf("message %q does not name %q", ae.Message, field)
	}
}

func TestDecodeAppliesDefaults(t *testing.T) {
	v, err := Decode(decAction(), json.RawMessage(`{"name":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &decParams{Status: "open", Limit: 1, Name: "n", Tags: []string{"a", "b"}}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got %+v", v)
	}
	v, err = Decode(decAction(), json.RawMessage(`{"name":"n","limit":5,"status":"done","tags":["z"],"on":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want = &decParams{Status: "done", Limit: 5, Name: "n", Tags: []string{"z"}, On: true}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got %+v", v)
	}
}

func TestDecodeEmptyAndNull(t *testing.T) {
	a := Action{Name: "x", Params: struct {
		N int `json:"n" default:"3"`
	}{}}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`null`)} {
		v, err := Decode(a, raw)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.ValueOf(v).Elem().Field(0).Int() != 3 {
			t.Fatalf("got %+v", v)
		}
	}
	v, err := Decode(Action{Name: "x"}, nil)
	if err != nil || v != nil {
		t.Fatalf("nil params: %v %v", v, err)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	_, err := Decode(decAction(), json.RawMessage(`{"name":"n","bogus":1}`))
	invalid(t, err, "bogus")
}

func TestDecodeRequired(t *testing.T) {
	_, err := Decode(decAction(), json.RawMessage(`{}`))
	invalid(t, err, "name")
}

func TestDecodeEnum(t *testing.T) {
	_, err := Decode(decAction(), json.RawMessage(`{"name":"n","status":"weird"}`))
	invalid(t, err, "status")
}

func TestDecodeWrongType(t *testing.T) {
	for _, tc := range []struct{ raw, field string }{
		{`{"name":"n","limit":"ten"}`, "limit"},
		{`{"name":"n","limit":1.5}`, "limit"},
		{`{"name":1}`, "name"},
		{`{"name":"n","on":"yes"}`, "on"},
		{`{"name":"n","tags":"a"}`, "tags"},
		{`[1]`, "object"},
		{`{bad`, "JSON"},
	} {
		_, err := Decode(decAction(), json.RawMessage(tc.raw))
		invalid(t, err, tc.field)
	}
}
