package cycle

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// logWriter turns the lines written to it into job log lines, keeping the
// warnings.
type logWriter struct {
	mu       sync.Mutex
	log      *slog.Logger
	buf      bytes.Buffer
	warnings []string
}

func newLogWriter(log *slog.Logger) *logWriter { return &logWriter{log: log} }

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// Incomplete line: keep it for the next write.
			l.buf.Reset()
			l.buf.WriteString(line)
			return len(p), nil
		}
		l.emit(strings.TrimRight(line, "\n"))
	}
}

func (l *logWriter) emit(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if strings.HasPrefix(line, "WARNING") {
		l.warnings = append(l.warnings, line)
		l.log.Warn(line)
		return
	}
	l.log.Info(line)
}

// flush logs a trailing partial line and returns the warnings.
func (l *logWriter) flush() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rest := l.buf.String(); rest != "" {
		l.buf.Reset()
		l.emit(rest)
	}
	return append([]string{}, l.warnings...)
}
