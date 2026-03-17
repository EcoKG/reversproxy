package logger

import (
	"log/slog"
	"os"
)

// Default is the package-level default logger tagged with component "app".
var Default = New("app")

// New creates a JSON-format slog.Logger tagged with the given component name.
// All log entries will include a "component" field set to the provided value.
func New(component string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler).With("component", component)
}

// With returns a new logger derived from the given logger with additional
// key-value pairs appended to every log entry.
func With(logger *slog.Logger, args ...any) *slog.Logger {
	return logger.With(args...)
}
