package usecase_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"
)

// hangResponseDelay bounds how long "hang" mode blocks before returning
// with no response written. It must be safely longer than any client
// Timeout used against this server in tests (100ms throughout this
// codebase's tests as of writing) so the client always gives up first,
// producing the intended ambiguous outcome. It is NOT driven purely by
// r.Context().Done(): empirically, an http.Client-side Timeout does not
// reliably propagate to the server's request context being canceled for
// a handler that is blocked without reading/writing anything (observed
// directly via a goroutine dump: the handler, and httptest.Server.Close,
// both stayed blocked well past 60s on this platform/Go version). Without
// this bound, httptest.Server.Close() — which waits for every connection
// to finish — would hang the entire test binary forever.
const hangResponseDelay = 400 * time.Millisecond

// FakeTelegramServer is a deterministic, in-process stand-in for the Bot
// API's sendMessage endpoint, giving tests full control over each of the
// four fault-injection points from ADR 0030:
//   - Mode "ok": responds 200 with a fresh, incrementing message_id —
//     bucket A (confirmed success).
//   - Mode "hang": accepts the connection and never responds — the
//     client's own timeout fires, producing bucket C (ambiguous).
//   - Mode "reject": responds 400 with a confirmed rejection — bucket B.
//
// Every call is counted, so tests can assert exactly how many times
// Telegram was actually contacted (the crux of "no blind resend").
//
// This lives here, in package usecase_test, rather than in the telegram
// package: its only consumers are this package's fault-injection and
// concurrency tests (publish_outbox_fault_injection_test.go and
// publish_outbox_concurrency_test.go), and it needs nothing unexported
// from the telegram package — just httptest plus a JSON literal of the
// same wire shape client.go already uses. Putting it in the telegram
// package instead (an earlier version of this file did, to work around a
// _test.go cross-package-visibility limitation) would have compiled the
// httptest package into the shipped phins-news-engine binary and understated
// that package's own test coverage against nothing it actually needs.
type FakeTelegramServer struct {
	*httptest.Server
	mode   atomic.Value // string: "ok" | "hang" | "reject"
	calls  int64
	nextID int64
}

// NewFakeTelegramServer starts a server in "ok" mode.
func NewFakeTelegramServer() *FakeTelegramServer {
	f := &FakeTelegramServer{}
	f.mode.Store("ok")
	f.nextID = 1000
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *FakeTelegramServer) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&f.calls, 1)
	mode, _ := f.mode.Load().(string)
	switch mode {
	case "hang":
		// Prefer real cancellation if the platform delivers it, but never
		// rely on it alone — see hangResponseDelay's doc comment.
		select {
		case <-r.Context().Done():
		case <-time.After(hangResponseDelay):
		}
		return
	case "reject":
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"})
	default:
		id := atomic.AddInt64(&f.nextID, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": id}})
	}
}

// SetMode switches behavior for subsequent requests.
func (f *FakeTelegramServer) SetMode(mode string) { f.mode.Store(mode) }

// Calls reports how many requests this server has received in total —
// the test's ground truth for "was Telegram actually contacted again."
func (f *FakeTelegramServer) Calls() int64 { return atomic.LoadInt64(&f.calls) }
