package action

import (
	"log/slog"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type portLogger struct{ l *slog.Logger }

func (g portLogger) Debug(msg string, kv ...any) { g.l.Debug(msg, kv...) }
func (g portLogger) Info(msg string, kv ...any)  { g.l.Info(msg, kv...) }
func (g portLogger) Warn(msg string, kv ...any)  { g.l.Warn(msg, kv...) }
func (g portLogger) Error(msg string, err error, kv ...any) {
	g.l.Error(msg, append([]any{"error", err}, kv...)...)
}

// PortLogger adapts a slog.Logger to the out.Logger port.
func PortLogger(l *slog.Logger) appout.Logger { return portLogger{l} }
