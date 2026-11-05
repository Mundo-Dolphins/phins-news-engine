package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// limitParam reads ?limit=, defaulting to 50 and capped at 500.
func limitParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 50
	}
	return min(n, 500)
}

func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	js, err := s.d.Store.ListJobs(r.Context(), limitParam(r))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]jobDTO, 0, len(js))
	for _, j := range js {
		out = append(out, toJob(j))
	}
	writeJSON(w, http.StatusOK, out)
}

// loadJob fetches a job, answering 404/500 itself when it cannot.
func (s *server) loadJob(w http.ResponseWriter, ctx context.Context, id string) (store.Job, bool) {
	j, ok, err := s.d.Store.GetJob(ctx, id)
	if err != nil {
		writeError(w, err)
		return j, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, action.CodeNotFound, "job not found")
		return j, false
	}
	return j, true
}

func (s *server) getJob(w http.ResponseWriter, r *http.Request) {
	if j, ok := s.loadJob(w, r.Context(), r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, toJob(j))
	}
}

func (s *server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Runner.Cancel(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	es, err := s.d.Store.ListAudit(r.Context(), limitParam(r))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]auditDTO, 0, len(es))
	for _, e := range es {
		out = append(out, toAudit(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// keepAlive is how often an idle event stream gets a comment line.
const keepAlive = 15 * time.Second

// jobEvents streams a job's log as server-sent events. It subscribes before
// reading history so no line falls in the gap; lines already replayed are
// skipped by sequence number.
func (s *server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	live, unsub := s.d.Runner.Subscribe(id)
	defer unsub()
	if _, ok := s.loadJob(w, ctx, id); !ok {
		return
	}
	last := int64(0)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("after"); v != "" {
		last, _ = strconv.ParseInt(v, 10, 64)
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(l store.LogLine) bool {
		last = l.Seq
		b, _ := json.Marshal(logDTO{Seq: l.Seq, At: l.At, Line: l.Line})
		if _, err := fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", l.Seq, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	// replay emits every stored line after last. The live channel is only a
	// wake-up signal, so lines it dropped for a slow client are never lost.
	replay := func() bool {
		ls, err := s.d.Store.Logs(ctx, id, last)
		if err != nil {
			return false
		}
		for _, l := range ls {
			if !emit(l) {
				return false
			}
		}
		return true
	}
	if !replay() {
		return
	}

	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for open := true; open; {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-live:
			if !ok {
				open = false
				break
			}
			if !replay() {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
	// The job ended; pick up lines a slow subscriber may have dropped.
	if !replay() {
		return
	}
	j, ok, err := s.d.Store.GetJob(ctx, id)
	if err != nil || !ok {
		return
	}
	b, _ := json.Marshal(toJob(j))
	_, _ = fmt.Fprintf(w, "event: end\ndata: %s\n\n", b)
	_ = rc.Flush()
}
