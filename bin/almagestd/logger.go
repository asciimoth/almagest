package main

import (
	"fmt"

	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

var (
	_ lib.Logger = &Logger{}
)

// TODO: Improve performance
type Logger struct {
	prefix string
}

func (l *Logger) Debug(args ...any) {
	// TODO: Check log level
	args = append([]any{l.prefix}, args...)
	golog.Debug(args...)
}

func (l *Logger) Debugf(format string, args ...any) {
	// TODO: Check log level
	golog.Debugf(l.prefix+" "+format, args...)
}

func (l *Logger) Info(args ...any) {
	// TODO: Check log level
	args = append([]any{l.prefix}, args...)
	golog.Info(args...)
}

func (l *Logger) Infof(format string, args ...any) {
	// TODO: Check log level
	golog.Infof(l.prefix+" "+format, args...)
}

func (l *Logger) Warn(args ...any) {
	// TODO: Check log level
	args = append([]any{l.prefix}, args...)
	golog.Warn(args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	// TODO: Check log level
	golog.Warnf(l.prefix+" "+format, args...)
}

func (l *Logger) Err(args ...any) {
	// TODO: Check log level
	args = append([]any{l.prefix}, args...)
	golog.Error(args...)
}

func (l *Logger) Errf(format string, args ...any) {
	// TODO: Check log level
	golog.Errorf(l.prefix+" "+format, args...)
}

func (l *Logger) Fatal(args ...any) {
	// TODO: Check log level
	args = append([]any{l.prefix}, args...)
	golog.Fatal(args...)
}

func (l *Logger) Fatalf(format string, args ...any) {
	// TODO: Check log level
	golog.Fatalf(l.prefix+" "+format, args...)
}

type LogFactory struct {
}

func (l *LogFactory) EngineLogger() lib.Logger {
	return &Logger{
		prefix: "[engine]",
	}
}

func (l *LogFactory) WorkspaceLogger() pasta.Logger {
	return &Logger{
		prefix: "[workspace]",
	}
}

func (l *LogFactory) NodeLogger(id uint64, class string) pasta.Logger {
	return &Logger{
		prefix: fmt.Sprintf("[%s %d]", class, id),
	}
}
