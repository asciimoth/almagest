package almagest

import "github.com/asciimoth/almagest/lib/logging"

type DebugLogLevel = logging.DebugLogLevel

const (
	DebugLogLevelDebug = logging.DebugLogLevelDebug
	DebugLogLevelInfo  = logging.DebugLogLevelInfo
	DebugLogLevelWarn  = logging.DebugLogLevelWarn
	DebugLogLevelError = logging.DebugLogLevelError
	DebugLogLevelFatal = logging.DebugLogLevelFatal
)

type DebugLogSource = logging.DebugLogSource
type DebugLogEntry = logging.DebugLogEntry
type DebugLogFactory = logging.DebugLogFactory
type DebugLogger = logging.DebugLogger

func NewDebugLogFactory(buffer int) *DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewDebugLogger(entries chan<- DebugLogEntry, source DebugLogSource) *DebugLogger {
	return logging.NewDebugLogger(entries, source)
}
