package httpapi

import (
	"encoding/json"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// jobDTO is the JSON form of a [store.Job].
type jobDTO struct {
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	Actor      string          `json:"actor"`
	Status     string          `json:"status"`
	Params     json.RawMessage `json:"params,omitempty"`
	Result     *action.Result  `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

func toJob(j store.Job) jobDTO {
	return jobDTO{ID: j.ID, Action: j.Action, Actor: j.Actor, Status: j.Status, Params: j.Params,
		Result: j.Result, Error: j.Error, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt}
}

// auditDTO is the JSON form of a [store.AuditEntry].
type auditDTO struct {
	ID      int64           `json:"id"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor"`
	Action  string          `json:"action"`
	Kind    string          `json:"kind"`
	Outcome string          `json:"outcome"`
	Error   string          `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func toAudit(e store.AuditEntry) auditDTO {
	return auditDTO{ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action, Kind: e.Kind,
		Outcome: e.Outcome, Error: e.Error, Params: e.Params}
}

// logDTO is the JSON form of a [store.LogLine].
type logDTO struct {
	Seq  int64     `json:"seq"`
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}

// catalogItem is one entry of the action catalog.
type catalogItem struct {
	Name        string        `json:"name"`
	Group       string        `json:"group"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Kind        action.Kind   `json:"kind"`
	Modes       []action.Mode `json:"modes"`
	LongRunning bool          `json:"long_running"`
	Exclusive   bool          `json:"exclusive"`
	Schema      any           `json:"schema"`
}
