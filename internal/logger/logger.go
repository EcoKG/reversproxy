package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Default is the package-level default logger tagged with component "app".
var Default = New("app")

// New creates a JSON-format slog.Logger tagged with the given component name.
// All log entries will include a "component" field set to the provided value.
// The log level defaults to Debug so all messages pass through; callers can
// use NewWithLevel to restrict output.
func New(component string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler).With("component", component)
}

// NewWithLevel creates a JSON-format slog.Logger with the given component name
// and a minimum log level parsed from levelStr (debug/info/warn/error).
// Unrecognised values default to info.
func NewWithLevel(component, levelStr string) *slog.Logger {
	lvl := parseLevel(levelStr)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler).With("component", component)
}

// parseLevel converts a human-readable level string to slog.Level.
// Unrecognised values default to slog.LevelInfo.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// With returns a new logger derived from the given logger with additional
// key-value pairs appended to every log entry.
func With(logger *slog.Logger, args ...any) *slog.Logger {
	return logger.With(args...)
}
