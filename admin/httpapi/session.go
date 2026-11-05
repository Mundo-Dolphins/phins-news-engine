package httpapi

import (
	"crypto/subtle"
	"net/http"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// login authenticates the single admin user. The limiter is consulted once
// per attempt, before the password is checked.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}
	ip, ok := auth.ClientIP(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden", "address not allowed")
		return
	}
	if !s.d.Limiter.Allow(ip) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.d.Username)) == 1
	passOK := auth.CheckPassword(s.d.PasswordHash, req.Password) // always run: constant-ish time
	if !userOK || !passOK {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	s.d.Limiter.Reset(ip)
	if err := s.d.Sessions.Login(r.Context(), w, s.d.Username); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.d.Sessions.Logout(r.Context(), w, r); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"username": u, "mode": s.d.Mode})
}
