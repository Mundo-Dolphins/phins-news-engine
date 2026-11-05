// Package jobs runs admin actions: synchronously for short operations, or as
// detached background jobs with captured logs, cancellation and a single
// exclusive slot.
package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// ErrBusy is returned when an Exclusive action is requested while another
// Exclusive action (sync or async) is running.
var ErrBusy = &action.Error{Code: action.CodeBusy, Message: "another exclusive job is running"}

// Job and audit outcome values.
const (
	statusRunning   = "running"
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCancelled = "cancelled"

	subscriberBuffer = 256
)

// Runner executes actions and tracks detached jobs.
type Runner struct {
	st   *store.Store
	base context.Context
	now  func() time.Time

	wg sync.WaitGroup

	mu        sync.Mutex
	exclusive bool
	runs      map[string]*run
}

// run is the in-memory state of one detached job.
type run struct {
	id     string
	cancel context.CancelFunc

	mu     sync.Mutex // guards subs and orders log append + fan-out
	subs   map[chan store.LogLine]struct{}
	closed bool
}

// New creates a Runner whose detached jobs are children of ctx; cancelling
// ctx cancels every running job. Jobs left queued or running in the store by a
// previous process are marked interrupted.
func New(ctx context.Context, st *store.Store, now func() time.Time) (*Runner, error) {
	if _, err := st.MarkRunningInterrupted(ctx, now()); err != nil {
		return nil, err
	}
	return &Runner{st: st, base: ctx, now: now, runs: map[string]*run{}}, nil
}

// Wait blocks until all detached jobs have finished.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) acquire(a action.Action) error {
	if !a.Exclusive {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exclusive {
		return ErrBusy
	}
	r.exclusive = true
	return nil
}

func (r *Runner) release(a action.Action) {
	if !a.Exclusive {
		return
	}
	r.mu.Lock()
	r.exclusive = false
	r.mu.Unlock()
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("admin jobs: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// execute calls a.Run, converting a panic into an error.
func execute(ctx context.Context, a action.Action, env action.Env, params any) (res action.Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = action.Result{}, fmt.Errorf("panic: %v", p)
		}
	}()
	return a.Run(ctx, env, params)
}

func outcome(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return statusSucceeded
	case ctx.Err() != nil:
		return statusCancelled
	default:
		return statusFailed
	}
}

// audit records finished write/destructive actions; reads are not audited.
func (r *Runner) audit(a action.Action, env action.Env, raw json.RawMessage, out string, runErr error) {
	if a.Kind != action.KindWrite && a.Kind != action.KindDestructive {
		return
	}
	e := store.AuditEntry{At: r.now(), Actor: env.Actor, Action: a.Name, Kind: string(a.Kind), Outcome: out, Params: raw}
	if runErr != nil {
		e.Error = runErr.Error()
	}
	if err := r.st.AddAudit(context.WithoutCancel(r.base), e); err != nil {
		slog.Error("admin audit failed", "action", a.Name, "err", err)
	}
}

// RunSync runs a in the calling goroutine without creating a job row.
// Exclusive actions need the exclusive slot and fail with [ErrBusy] if taken.
// rawParams is only used for the audit entry of write/destructive actions.
// A nil env.Log defaults to slog.Default().
func (r *Runner) RunSync(ctx context.Context, a action.Action, env action.Env, params any, rawParams json.RawMessage) (action.Result, error) {
	if err := r.acquire(a); err != nil {
		return action.Result{}, err
	}
	defer r.release(a)
	if env.Log == nil {
		env.Log = slog.Default()
	}
	if env.Now == nil {
		env.Now = r.now
	}
	res, err := execute(ctx, a, env, params)
	r.audit(a, env, rawParams, outcome(ctx, err), err)
	return res, err
}

// Start runs a as a detached job, a child of the runner's base context and
// independent of any request. The job is persisted as running and its slog
// records are appended to the job log. Exclusive actions fail with [ErrBusy]
// if the slot is taken.
func (r *Runner) Start(a action.Action, env action.Env, params any, rawParams json.RawMessage) (store.Job, error) {
	if err := r.acquire(a); err != nil {
		return store.Job{}, err
	}
	id, err := newID()
	if err != nil {
		r.release(a)
		return store.Job{}, err
	}
	started := r.now()
	job := store.Job{ID: id, Action: a.Name, Actor: env.Actor, Status: statusRunning, Params: rawParams, CreatedAt: started, StartedAt: &started}
	if err := r.st.CreateJob(context.WithoutCancel(r.base), job); err != nil {
		r.release(a)
		return store.Job{}, err
	}

	ctx, cancel := context.WithCancel(r.base)
	rn := &run{id: id, cancel: cancel, subs: map[chan store.LogLine]struct{}{}}
	r.mu.Lock()
	r.runs[id] = rn
	r.mu.Unlock()

	if env.Now == nil {
		env.Now = r.now
	}
	env.Log = slog.New(newLogHandler(func(line string) { r.appendLog(rn, line) }))
	r.wg.Add(1)
	go r.finish(ctx, rn, job, a, env, params, rawParams)
	return job, nil
}

// finish runs the action, persists the outcome and releases the job's resources.
func (r *Runner) finish(ctx context.Context, rn *run, job store.Job, a action.Action, env action.Env, params any, raw json.RawMessage) {
	defer r.wg.Done()
	res, err := execute(ctx, a, env, params)
	out := outcome(ctx, err)

	fin := r.now()
	job.Status, job.FinishedAt = out, &fin
	if err != nil {
		job.Error = err.Error()
	} else {
		job.Result = &res
	}
	if uerr := r.st.UpdateJob(context.WithoutCancel(r.base), job); uerr != nil {
		slog.Error("admin job update failed", "job", job.ID, "err", uerr)
	}
	r.audit(a, env, raw, out, err)

	rn.cancel()
	rn.close()
	r.mu.Lock()
	delete(r.runs, job.ID)
	if a.Exclusive {
		r.exclusive = false
	}
	r.mu.Unlock()
}

// Cancel requests cancellation of a running job. An unknown job yields a
// not_found error, a finished one a conflict error.
func (r *Runner) Cancel(id string) error {
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if ok {
		rn.cancel()
		return nil
	}
	_, found, err := r.st.GetJob(context.WithoutCancel(r.base), id)
	if err != nil {
		return err
	}
	if !found {
		return action.NotFound(fmt.Errorf("job %q not found", id))
	}
	return action.Conflict(fmt.Errorf("job %q is not running", id))
}

// Subscribe returns a channel of log lines appended to the job after the
// call, and a function to unsubscribe. The channel is closed when the job
// ends; for an unknown or already finished job it is returned closed. Late
// subscribers should replay history with store.Logs first. Lines are dropped
// for a subscriber that falls more than 256 lines behind.
func (r *Runner) Subscribe(id string) (<-chan store.LogLine, func()) {
	ch := make(chan store.LogLine, subscriberBuffer)
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		close(ch)
		return ch, func() {}
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if rn.closed {
		close(ch)
		return ch, func() {}
	}
	rn.subs[ch] = struct{}{}
	return ch, func() {
		rn.mu.Lock()
		defer rn.mu.Unlock()
		if _, ok := rn.subs[ch]; ok {
			delete(rn.subs, ch)
			close(ch)
		}
	}
}

func (rn *run) close() {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.closed = true
	for ch := range rn.subs {
		close(ch)
		delete(rn.subs, ch)
	}
}

// appendLog persists a line and fans it out; the run lock keeps sequence order.
func (r *Runner) appendLog(rn *run, line string) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	at := r.now()
	seq, err := r.st.AppendLog(context.WithoutCancel(r.base), rn.id, line, at)
	if err != nil {
		slog.Error("admin job log append failed", "job", rn.id, "err", err)
		return
	}
	l := store.LogLine{Seq: seq, At: at, Line: line}
	for ch := range rn.subs {
		select {
		case ch <- l:
		default:
		}
	}
}

// logHandler renders each record as its message followed by its attributes as
// key=value (slog.TextHandler formatting, without time and level), hands the
// line to sink, and forwards the record to slog.Default().
type logHandler struct {
	text slog.Handler
	buf  *bytes.Buffer
	mu   *sync.Mutex
	sink func(string)
}

func newLogHandler(sink func(string)) *logHandler {
	buf := &bytes.Buffer{}
	text := slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey) {
				return slog.Attr{}
			}
			return a
		},
	})
	return &logHandler{text: text, buf: buf, mu: &sync.Mutex{}, sink: sink}
}

func (h *logHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logHandler) Handle(ctx context.Context, rec slog.Record) error {
	h.mu.Lock()
	h.buf.Reset()
	err := h.text.Handle(ctx, rec)
	attrs := strings.TrimSpace(h.buf.String())
	h.mu.Unlock()
	line := rec.Message
	if attrs != "" {
		if line != "" {
			line += " "
		}
		line += attrs
	}
	h.sink(line)
	if d := slog.Default().Handler(); d.Enabled(ctx, rec.Level) {
		err = errors.Join(err, d.Handle(ctx, rec.Clone()))
	}
	return err
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.text = h.text.WithAttrs(attrs)
	return &c
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.text = h.text.WithGroup(name)
	return &c
}
