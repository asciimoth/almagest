package lib

import (
	"github.com/asciimoth/pasta/pasta"
)

// Logger receives workspace diagnostics.
//
// Implementations must be safe for concurrent use.
type Logger interface {
	// Debug logs a debug message.
	Debug(args ...any)
	// Debugf logs a formatted debug message.
	Debugf(format string, args ...any)
	// Info logs an informational message.
	Info(args ...any)
	// Infof logs a formatted informational message.
	Infof(format string, args ...any)
	// Warn logs a warning message.
	Warn(args ...any)
	// Warnf logs a formatted warning message.
	Warnf(format string, args ...any)
	// Err logs an error message.
	Err(args ...any)
	// Errf logs a formatted error message.
	Errf(format string, args ...any)
	// Fatal logs a fatal message.
	Fatal(args ...any)
	// Fatalf logs a formatted fatal message.
	Fatalf(format string, args ...any)
}

// LogFactory creates loggers for engine, nodes, and pasta workspace
type LogFactory interface {
	pasta.LogFactory

	EngineLogger() Logger
}
