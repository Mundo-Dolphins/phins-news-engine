package shared

import "github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"

var (
	ConfigPath  string
	ProfileFlag string

	// FetcherExtensions supplies the optional fetch-category builders used
	// by commands that call fetcherwiring.Build. The host program registers
	// it via SetFetcherExtensions.
	FetcherExtensions fetcherwiring.Extensions
)

// SetFetcherExtensions registers the fetch-category extensions.
func SetFetcherExtensions(e fetcherwiring.Extensions) { FetcherExtensions = e }

// DiscardLogger is a no-op out.Logger-style logger for command paths that
// need to satisfy infrastructure constructors without emitting logs.
type DiscardLogger struct{}

func (DiscardLogger) Debug(string, ...any)        {}
func (DiscardLogger) Info(string, ...any)         {}
func (DiscardLogger) Warn(string, ...any)         {}
func (DiscardLogger) Error(string, error, ...any) {}
