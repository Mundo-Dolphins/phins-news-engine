package action_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
)

func TestRefusePathOverrides(t *testing.T) {
	p := action.PathParam{Name: "dir", Value: "other/dir", Configured: "data/model"}
	if err := action.RefusePathOverrides(action.Env{Mode: action.ModeLocal}, p); err != nil {
		t.Fatalf("local = %v, want nil", err)
	}
	prod := action.Env{Mode: action.ModeProduction}
	for name, ok := range map[string]action.PathParam{
		"empty":      {Name: "dir", Value: "", Configured: "data/model"},
		"configured": {Name: "dir", Value: "data/model", Configured: "data/model"},
	} {
		if err := action.RefusePathOverrides(prod, ok); err != nil {
			t.Errorf("%s = %v, want nil", name, err)
		}
	}
	err := action.RefusePathOverrides(prod, action.PathParam{Name: "ok", Value: "x", Configured: "x"}, p)
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInvalidParams {
		t.Fatalf("production override = %v, want invalid_params", err)
	}
	if !strings.Contains(ae.Message, "dir") {
		t.Errorf("message %q does not name the param", ae.Message)
	}
}
