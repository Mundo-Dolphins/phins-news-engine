package action

import (
	"context"
	"testing"
)

func noop(context.Context, Env, any) (Result, error) { return Result{}, nil }

func TestRegistryForModeFiltersAndSorts(t *testing.T) {
	var r Registry
	r.Add(
		Action{Name: "train.run", Group: "train", Modes: []Mode{ModeLocal}, Run: noop},
		Action{Name: "claims.retry", Group: "claims", Modes: []Mode{ModeLocal, ModeProduction}, Run: noop},
		Action{Name: "claims.list", Group: "claims", Modes: []Mode{ModeLocal, ModeProduction}, Run: noop},
	)
	prod := r.For(ModeProduction)
	if len(prod) != 2 || prod[0].Name != "claims.list" || prod[1].Name != "claims.retry" {
		t.Fatalf("production = %+v", prod)
	}
	local := r.For(ModeLocal)
	if len(local) != 3 || local[2].Name != "train.run" {
		t.Fatalf("local = %+v", local)
	}
	if a, ok := r.Get("train.run"); !ok || a.Group != "train" {
		t.Fatalf("Get = %+v %v", a, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get found missing action")
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	mustPanic := func(name string, a ...Action) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: expected panic", name)
			}
		}()
		var r Registry
		r.Add(a...)
	}
	ok := Action{Name: "a", Modes: []Mode{ModeLocal}, Run: noop}
	mustPanic("duplicate", ok, ok)
	mustPanic("empty name", Action{Modes: []Mode{ModeLocal}, Run: noop})
	mustPanic("no modes", Action{Name: "a", Run: noop})
	mustPanic("nil run", Action{Name: "a", Modes: []Mode{ModeLocal}})
	mustPanic("swaps without DB", Action{Name: "a", Modes: []Mode{ModeLocal}, Run: noop, SwapsDB: true, NoOperationalDB: true})
	mustPanic("bad params", Action{Name: "a", Modes: []Mode{ModeLocal}, Run: noop, Params: struct {
		X map[string]string `json:"x"`
	}{}})
}

func TestRegistryBadEnumPanics(t *testing.T) {
	for name, p := range map[string]any{
		"enum on int": struct {
			N int `json:"n" enum:"1,2"`
		}{},
		"default outside enum": struct {
			S string `json:"s" enum:"a,b" default:"c"`
		}{},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: expected panic", name)
				}
			}()
			var r Registry
			r.Add(Action{Name: "a", Modes: []Mode{ModeLocal}, Run: noop, Params: p})
		}()
	}
}
