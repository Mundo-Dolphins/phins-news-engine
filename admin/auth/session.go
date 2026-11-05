package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/store"
)

// CookieName is the session cookie name.
const CookieName = "phins_admin_session"

type userKey struct{}

// Sessions issues and validates cookie sessions backed by the admin store.
type Sessions struct {
	st  *store.Store
	ttl time.Duration
	now func() time.Time
}

// NewSessions returns a session manager with a sliding ttl.
func NewSessions(st *store.Store, ttl time.Duration, now func() time.Time) *Sessions {
	return &Sessions{st: st, ttl: ttl, now: now}
}

// UserFrom returns the username Middleware stored in ctx.
func UserFrom(ctx context.Context) (string, bool) {
	u, ok := ctx.Value(userKey{}).(string)
	return u, ok
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Sessions) setCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(s.ttl / time.Second),
		HttpOnly: true,
		Secure:   false, // plain HTTP on the LAN
		SameSite: http.SameSiteStrictMode,
	})
}

// Login creates a session for username and sets the session cookie.
func (s *Sessions) Login(ctx context.Context, w http.ResponseWriter, username string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("auth: generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.st.CreateSession(ctx, hashToken(token), username, s.now().Add(s.ttl)); err != nil {
		return err
	}
	s.setCookie(w, token)
	return nil
}

// Logout deletes the request's session (if any) and clears the cookie.
func (s *Sessions) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	return s.st.DeleteSession(ctx, hashToken(c.Value))
}

// Middleware requires a valid session. On success it extends the session
// expiry (sliding), refreshes the cookie and stores the username in the
// request context; otherwise it answers 401 JSON.
func (s *Sessions) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		now := s.now()
		user, ok, err := s.st.TouchSession(r.Context(), hashToken(c.Value), now, now.Add(s.ttl))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "session lookup failed")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		s.setCookie(w, c.Value)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}
