// Package logging adapts zerolog to the application's out.Logger port so
// structured logging stays consistent across the service and tools binaries.
package logging

import (
	"io"
	"os"

	"github.com/rs/zerolog"

	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Logger wraps a zerolog.Logger to implement out.Logger.
type Logger struct {
	zl zerolog.Logger
}

// New builds a Logger writing to stdout at the given level. When pretty is
// true, output is human-readable console text instead of JSON.
func New(level string, pretty bool) *Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	var w io.Writer = os.Stdout
	if pretty {
		w = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}
	}

	return &Logger{zl: zerolog.New(w).Level(lvl).With().Timestamp().Logger()}
}

// Debug logs a debug-level message with optional key/value pairs.
func (l *Logger) Debug(msg string, kv ...any) {
	withFields(l.zl.Debug(), kv).Msg(msg)
}

// Info logs an informational message with optional key/value pairs.
func (l *Logger) Info(msg string, kv ...any) {
	withFields(l.zl.Info(), kv).Msg(msg)
}

// Warn logs a warning message with optional key/value pairs.
func (l *Logger) Warn(msg string, kv ...any) {
	withFields(l.zl.Warn(), kv).Msg(msg)
}

// Error logs an error message with optional key/value pairs.
func (l *Logger) Error(msg string, err error, kv ...any) {
	withFields(l.zl.Error().Err(err), kv).Msg(msg)
}

func withFields(e *zerolog.Event, kv []any) *zerolog.Event {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		e = e.Interface(key, kv[i+1])
	}
	return e
}

var _ out.Logger = (*Logger)(nil)
