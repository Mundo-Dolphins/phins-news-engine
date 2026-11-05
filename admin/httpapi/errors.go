package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
)

// maxBody is the largest accepted JSON request body.
const maxBody = 1 << 20

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("admin http: write response", "err", err)
	}
}

// writeErr writes the standard error envelope.
func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// statusFor maps an action error code to an HTTP status.
func statusFor(code string) int {
	switch code {
	case action.CodeInvalidParams, action.CodeConfirmRequired:
		return http.StatusBadRequest
	case action.CodeNotFound, action.CodeUnavailableInMode:
		return http.StatusNotFound
	case action.CodeConflict, action.CodeBusy:
		return http.StatusConflict
	case action.CodeSchemaMismatch:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// writeError maps err to a response. Action errors keep their code and
// message; anything else is logged and answered with a generic 500.
func writeError(w http.ResponseWriter, err error) {
	var ae *action.Error
	if errors.As(err, &ae) {
		if st := statusFor(ae.Code); st != http.StatusInternalServerError {
			writeErr(w, st, ae.Code, ae.Error())
			return
		}
	}
	slog.Error("admin http: internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

// readJSON decodes a size-limited JSON body into dst. An empty body leaves
// dst untouched. It writes the error response and returns false on failure.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return true
		case errors.As(err, &mbe):
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		default:
			writeErr(w, http.StatusBadRequest, "invalid_params", "invalid JSON body")
		}
		return false
	}
	return true
}
