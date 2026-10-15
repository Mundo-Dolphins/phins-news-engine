package out

// Logger is a small structured-logging port so the application layer never
// depends on a concrete logging library.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, err error, kv ...any)
}
