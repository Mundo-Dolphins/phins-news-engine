package validation_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/validation"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

// settings writes a valid settings file (catalogs from the repository
// examples, rss enabled) and returns its path.
func settings(t *testing.T) string {
	t.Helper()
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	path := testsupport.WriteFixture(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.ReplaceAll(string(body), "configs/sources.yml", repofs.ExampleConfig(t, "sources.yml"))
	out = strings.ReplaceAll(out, "configs/source-policies.yml", repofs.ExampleConfig(t, "source-policies.yml"))
	out += "\nfetch_sources:\n  rss: {enabled: true}\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envFor(t *testing.T, mode action.Mode) action.Env {
	t.Helper()
	path := settings(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return action.Env{Config: cfg, Mode: mode, ConfigPath: path, Profile: config.ProfileLocal}
}

func run(t *testing.T, env action.Env, name, params string) (action.Result, error) {
	t.Helper()
	for _, a := range validation.Actions() {
		if a.Name != name {
			continue
		}
		p, err := action.Decode(a, json.RawMessage(params))
		if err != nil {
			return action.Result{}, err
		}
		return a.Run(context.Background(), env, p)
	}
	t.Fatalf("no action %q", name)
	return action.Result{}, nil
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func writeModel(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate", Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, `{"f1":0.5}`, schema); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return dir
}

func TestMetadata(t *testing.T) {
	want := map[string]string{"config.validate": "config", "model.validate": "model", "clustering.rules_validate": "clustering"}
	as := validation.Actions()
	if len(as) != len(want) {
		t.Fatalf("got %d actions", len(as))
	}
	var reg action.Registry
	reg.Add(as...)
	for _, a := range as {
		if want[a.Name] != a.Group || a.Kind != action.KindRead || len(a.Modes) != 2 || a.LongRunning || a.Exclusive || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", a.Name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for name, p := range props {
			if prop, _ := p.(map[string]any); prop["description"] == nil {
				t.Errorf("%s param %s has no help", a.Name, name)
			}
		}
	}
}

func TestConfigValidateOK(t *testing.T) {
	env := envFor(t, action.ModeProduction)
	res, err := run(t, env, "config.validate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "config OK: "+env.ConfigPath+" (profile=local)" {
		t.Errorf("summary = %q", res.Summary)
	}
	b, _ := json.Marshal(res.Object)
	if !strings.Contains(string(b), `"active_sources":["rss"]`) || !strings.Contains(string(b), `"retention"`) {
		t.Errorf("object = %s", b)
	}
}

func TestConfigValidateReadsFileNotEnvConfig(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	body, err := os.ReadFile(env.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "audit_events_days: 30", "audit_events_days: 77", 1)
	if edited == string(body) {
		t.Fatal("fixture has no audit_events_days: 30 to edit")
	}
	if err := os.WriteFile(env.ConfigPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, env, "config.validate", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	if !strings.Contains(string(b), `"audit_events_days":77`) {
		t.Errorf("object = %s, want the edited file's value", b)
	}
}

func TestConfigValidateBrokenFile(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	if err := os.WriteFile(env.ConfigPath, []byte("sqlite: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, env, "config.validate", `{}`)
	if err != nil {
		t.Fatalf("err = %v, want a FAILED result", err)
	}
	if !strings.HasPrefix(res.Summary, "FAILED: ") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestModelValidate(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	dir := writeModel(t)
	res, err := run(t, env, "model.validate", `{"dir":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Object)
	for _, want := range []string{`"directory":"` + dir + `"`, `"algorithm":"naive_bayes"`, `"features":1`, `"metrics":{"f1":0.5}`, `"accept":80`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("object = %s, missing %s", b, want)
		}
	}
}

func TestModelValidateMissingDirIsNotFound(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	_, err := run(t, env, "model.validate", `{"dir":"`+filepath.Join(t.TempDir(), "nope")+`"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestModelValidateEmptyConfig(t *testing.T) {
	env := envFor(t, action.ModeProduction)
	env.Config.Model.Path = ""
	_, err := run(t, env, "model.validate", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestModelValidateRefusesPathInProduction(t *testing.T) {
	env := envFor(t, action.ModeProduction)
	env.Config.Model.Path = writeModel(t)
	_, err := run(t, env, "model.validate", `{"dir":"/etc"}`)
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "dir") {
		t.Errorf("err = %v, want it to name dir", err)
	}
	if _, err := run(t, env, "model.validate", `{"dir":"`+env.Config.Model.Path+`"}`); err != nil {
		t.Errorf("configured dir passed explicitly: %v", err)
	}
	if _, err := run(t, env, "model.validate", `{}`); err != nil {
		t.Errorf("default dir: %v", err)
	}
}

func TestRulesValidate(t *testing.T) {
	env := envFor(t, action.ModeProduction)
	env.Config.Clustering.RulesPath = repofs.ExampleConfig(t, "event_clustering_rules.yml")
	for _, params := range []string{`{}`, `{"path":"` + env.Config.Clustering.RulesPath + `"}`} {
		res, err := run(t, env, "clustering.rules_validate", params)
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		b, _ := json.Marshal(res.Object)
		if !strings.Contains(string(b), `"entities":`) || !strings.Contains(string(b), `"version":`) {
			t.Errorf("object = %s", b)
		}
	}
	_, err := run(t, env, "clustering.rules_validate", `{"path":"/etc/passwd"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestRulesValidateEmptyConfig(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	env.Config.Clustering.RulesPath = ""
	_, err := run(t, env, "clustering.rules_validate", `{}`)
	wantCode(t, err, action.CodeConflict)
}

func TestRulesValidateBrokenFileInLocal(t *testing.T) {
	env := envFor(t, action.ModeLocal)
	bad := filepath.Join(t.TempDir(), "rules.yml")
	if err := os.WriteFile(bad, []byte("version: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, env, "clustering.rules_validate", `{"path":"`+bad+`"}`); err == nil {
		t.Error("want an error for an invalid rules file")
	}
}
