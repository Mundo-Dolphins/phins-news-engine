// Package httpapi is the admin panel's HTTP surface: the REST API, SSE job
// events, the MCP mount and the embedded SPA.
package httpapi

import (
	"io/fs"
	"net/http"
	"net/netip"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/jobs"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// Deps are the collaborators of the HTTP handler.
type Deps struct {
	Registry *action.Registry
	Runner   *jobs.Runner
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.LoginLimiter
	// EnvFor builds the action environment for an actor ("user:<name>").
	EnvFor func(actor string) action.Env
	// Username and PasswordHash (bcrypt) identify the single admin user.
	Username, PasswordHash string
	// WritesBlocked, when non-nil, rejects write and destructive actions
	// with this error (schema mismatch in production).
	WritesBlocked error
	// Assets is the SPA build, already rooted at the dist directory.
	Assets fs.FS
	// MCP is mounted at /mcp behind the bearer token; nil disables it.
	MCP  http.Handler
	Mode action.Mode
}

type server struct{ d Deps }

// NewHandler returns the admin HTTP handler. Every route is restricted to
// the allowed networks. /api/* needs a session (except logging in) and a
// same-origin request; /healthz, static assets and /mcp use no session.
func NewHandler(d Deps, allowed []netip.Prefix, mcpToken string) http.Handler {
	s := &server{d: d}

	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/session", s.whoami)
	protected.HandleFunc("GET /api/actions", s.catalog)
	protected.HandleFunc("POST /api/actions/{name}", s.runAction)
	protected.HandleFunc("GET /api/jobs", s.listJobs)
	protected.HandleFunc("GET /api/jobs/{id}", s.getJob)
	protected.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	protected.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	protected.HandleFunc("GET /api/audit", s.listAudit)
	protected.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, action.CodeNotFound, "no such endpoint")
	})

	api := http.NewServeMux()
	api.HandleFunc("POST /api/session", s.login)
	api.HandleFunc("DELETE /api/session", s.logout)
	api.Handle("/api/", d.Sessions.Middleware(protected))

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.healthz)
	root.Handle("/api/", auth.SameOrigin(api))
	if d.MCP != nil {
		root.Handle("/mcp", auth.Bearer(mcpToken, d.MCP))
	}
	root.Handle("/", spaHandler(d.Assets))
	return auth.LANOnly(allowed, root)
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	schema := "ok"
	if s.d.WritesBlocked != nil {
		schema = "mismatch"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": string(s.d.Mode), "schema": schema})
}
