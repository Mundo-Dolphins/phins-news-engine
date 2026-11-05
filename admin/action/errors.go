// Package action is the registry of admin actions. Each action is declared
// once; the REST API, the MCP server and the SPA form generator all derive
// from it (JSON Schema from the params struct tags, decoding with defaults
// and validation).
package action

import "fmt"

// Error codes carried by [Error].
const (
	// CodeInvalidParams marks params that failed decoding or validation.
	CodeInvalidParams = "invalid_params"
	// CodeNotFound marks a missing target entity.
	CodeNotFound = "not_found"
	// CodeConflict marks a state that forbids the requested change.
	CodeConflict = "conflict"
	// CodeConfirmRequired marks a destructive action called without confirm.
	CodeConfirmRequired = "confirmation_required"
	// CodeSchemaMismatch marks an operational DB schema that does not match the binary.
	CodeSchemaMismatch = "schema_mismatch"
	// CodeUnavailableInMode marks an action not offered in the current mode.
	CodeUnavailableInMode = "unavailable_in_mode"
	// CodeBusy marks an exclusive job already running.
	CodeBusy = "busy"
)

// Error is an action failure with a stable machine-readable code.
type Error struct {
	Code    string
	Message string
	Err     error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.Err }

func newError(code string, err error) *Error {
	return &Error{Code: code, Message: err.Error(), Err: err}
}

// NotFound wraps err as a [CodeNotFound] error.
func NotFound(err error) *Error { return newError(CodeNotFound, err) }

// Conflict wraps err as a [CodeConflict] error.
func Conflict(err error) *Error { return newError(CodeConflict, err) }

func invalidParams(format string, args ...any) *Error {
	return &Error{Code: CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}
