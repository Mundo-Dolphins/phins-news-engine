// Package validation holds the admin actions that check configuration
// artifacts without changing them: the settings file, an editorial model
// directory and a clustering rules file. They mirror the `config validate`,
// `validate-model` and `clustering rules validate` CLI commands.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/configcheck"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

var errBadParams = errors.New("validation: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

type noParams struct{}

type modelParams struct {
	Dir string `json:"dir" help:"model directory to verify (default model.path)" format:"path"`
}

type rulesParams struct {
	Path string `json:"path" help:"clustering rules YAML file (default clustering.rules_path)" format:"path"`
}

// Actions returns config.validate, model.validate and
// clustering.rules_validate.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "config.validate", Group: "config", Title: "Validate configuration",
			Description: "Re-reads the settings file from disk and validates it strictly for the active profile, reporting the active fetch sources. A broken file is reported in the result, not as an error.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runConfigValidate,
		},
		{
			Name: "model.validate", Group: "model", Title: "Validate editorial model",
			Description: "Verifies the integrity of an editorial-classifier model directory (checksums, manifest, feature schema) and reports its manifest and metrics.",
			Kind:        action.KindRead, Modes: bothModes, Params: modelParams{}, Run: runModelValidate,
		},
		{
			Name: "clustering.rules_validate", Group: "clustering", Title: "Validate clustering rules",
			Description: "Validates a clustering rules YAML file and reports its version, name and entity count.",
			Kind:        action.KindRead, Modes: bothModes, Params: rulesParams{}, Run: runRulesValidate,
		},
	}
}

type retentionJSON struct {
	RejectedArticlesDays    int `json:"rejected_articles_days"`
	DuplicatesDays          int `json:"duplicates_days"`
	AuditEventsDays         int `json:"audit_events_days"`
	PublicationAttemptsDays int `json:"publication_attempts_days"`
	ClustersDays            int `json:"clusters_days"`
}

type configJSON struct {
	Config          string            `json:"config"`
	Profile         string            `json:"profile"`
	ActiveSources   []string          `json:"active_sources"`
	Inactive        map[string]string `json:"inactive"`
	SQLitePath      string            `json:"sqlite_path"`
	CheckInterval   string            `json:"scheduler_check_interval"`
	CleanupInterval string            `json:"scheduler_cleanup_interval"`
	Retention       retentionJSON     `json:"retention"`
}

func runConfigValidate(_ context.Context, env action.Env, _ any) (action.Result, error) {
	// The file is the subject, so it is re-read rather than using env.Config:
	// an edit made since the admin started must show up here.
	rep, err := configcheck.Validate(env.ConfigPath, env.Profile, env.FetcherExtensions)
	if err != nil {
		return action.Result{
			Render:  "object",
			Object:  map[string]any{"config": env.ConfigPath, "profile": string(env.Profile), "ok": false, "error": err.Error()},
			Summary: "FAILED: " + err.Error(),
		}, nil
	}
	cfg := rep.Config
	active := rep.Active
	if active == nil {
		active = []string{}
	}
	keys := make([]string, 0, len(rep.Inactive))
	for k := range rep.Inactive {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	inactive := make(map[string]string, len(keys))
	for _, k := range keys {
		inactive[k] = rep.Inactive[k]
	}
	return action.Result{
		Render: "object",
		Object: configJSON{
			Config: env.ConfigPath, Profile: string(rep.Profile), ActiveSources: active, Inactive: inactive,
			SQLitePath:      cfg.SQLite.Path,
			CheckInterval:   cfg.Scheduler.CheckInterval().String(),
			CleanupInterval: cfg.Scheduler.CleanupInterval().String(),
			Retention: retentionJSON{
				RejectedArticlesDays: cfg.Retention.RejectedArticlesDays, DuplicatesDays: cfg.Retention.DuplicatesDays,
				AuditEventsDays: cfg.Retention.AuditEventsDays, PublicationAttemptsDays: cfg.Retention.PublicationAttemptsDays,
				ClustersDays: cfg.Retention.ClustersDays,
			},
		},
		Summary: fmt.Sprintf("config OK: %s (profile=%s)", env.ConfigPath, rep.Profile),
	}, nil
}

type thresholdsJSON struct {
	Reject float64 `json:"reject"`
	Accept float64 `json:"accept"`
}

type modelJSON struct {
	Directory            string         `json:"directory"`
	Version              string         `json:"version"`
	Algorithm            string         `json:"algorithm"`
	Status               string         `json:"status"`
	FeatureSchemaVersion string         `json:"feature_schema_version"`
	Features             int            `json:"features"`
	Thresholds           thresholdsJSON `json:"thresholds"`
	DatasetHash          string         `json:"dataset_hash"`
	Metrics              any            `json:"metrics"`
}

func runModelValidate(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*modelParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("validation: no configuration loaded")
	}
	if err := action.RefusePathOverrides(env, action.PathParam{Name: "dir", Value: p.Dir, Configured: env.Config.Model.Path}); err != nil {
		return action.Result{}, err
	}
	dir := p.Dir
	if dir == "" {
		dir = env.Config.Model.Path
	}
	if dir == "" {
		return action.Result{}, action.Conflict(errors.New("model.path is empty; pass dir"))
	}
	manifest, schema, metricsJSON, err := editorialmodel.Inspect(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	var metrics any = metricsJSON
	if json.Valid([]byte(metricsJSON)) {
		metrics = json.RawMessage(metricsJSON)
	}
	return action.Result{
		Render: "object",
		Object: modelJSON{
			Directory: dir, Version: manifest.Version, Algorithm: manifest.Algorithm, Status: manifest.Status,
			FeatureSchemaVersion: schema.Version, Features: len(schema.Features),
			Thresholds:  thresholdsJSON{Reject: manifest.Thresholds.Reject, Accept: manifest.Thresholds.Accept},
			DatasetHash: manifest.DatasetHash, Metrics: metrics,
		},
		Summary: fmt.Sprintf("model %s (%s, %s) verified", manifest.Version, manifest.Algorithm, manifest.Status),
	}, nil
}

type rulesJSON struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Name     string `json:"name"`
	Entities int    `json:"entities"`
}

func runRulesValidate(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*rulesParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("validation: no configuration loaded")
	}
	configured := env.Config.Clustering.RulesPath
	if err := action.RefusePathOverrides(env, action.PathParam{Name: "path", Value: p.Path, Configured: configured}); err != nil {
		return action.Result{}, err
	}
	path := p.Path
	if path == "" {
		path = configured
	}
	if path == "" {
		return action.Result{}, action.Conflict(errors.New("clustering.rules_path is empty; pass path"))
	}
	rf, err := clusteringrules.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render:  "object",
		Object:  rulesJSON{Path: path, Version: rf.Version, Name: rf.Name, Entities: len(rf.Entities)},
		Summary: "clustering rules OK: " + path,
	}, nil
}
