package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Bearer requires "Authorization: Bearer <token>" matching token in
// constant time. An empty configured token never authenticates.
func Bearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, found := strings.Cut(r.Header.Get("Authorization"), " ")
		ok := found && strings.EqualFold(scheme, "Bearer")
		if ok && token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing bearer token")
	})
}
