package main

import (
	"fmt"
	"os"

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
	level  logLevelEnabled
}

type logLevelEnabled struct {
	debug bool
	info  bool
	warn  bool
	err   bool
	fatal bool
}

func newLogLevelEnabled(level golog.Level) logLevelEnabled {
	return logLevelEnabled{
		debug: level >= golog.DebugLevel,
		info:  level >= golog.InfoLevel,
		warn:  level >= golog.WarnLevel,
		err:   level >= golog.ErrorLevel,
		fatal: level >= golog.FatalLevel,
	}
}

func (l *Logger) Debug(args ...any) {
	if !l.level.debug {
		return
	}

	args = append([]any{l.prefix}, args...)
	golog.Debug(args...)
}

func (l *Logger) Debugf(format string, args ...any) {
	if !l.level.debug {
		return
	}

	golog.Debugf(l.prefix+" "+format, args...)
}

func (l *Logger) Info(args ...any) {
	if !l.level.info {
		return
	}

	args = append([]any{l.prefix}, args...)
	golog.Info(args...)
}

func (l *Logger) Infof(format string, args ...any) {
	if !l.level.info {
		return
	}

	golog.Infof(l.prefix+" "+format, args...)
}

func (l *Logger) Warn(args ...any) {
	if !l.level.warn {
		return
	}

	args = append([]any{l.prefix}, args...)
	golog.Warn(args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	if !l.level.warn {
		return
	}

	golog.Warnf(l.prefix+" "+format, args...)
}

func (l *Logger) Err(args ...any) {
	if !l.level.err {
		return
	}

	args = append([]any{l.prefix}, args...)
	golog.Error(args...)
}

func (l *Logger) Errf(format string, args ...any) {
	if !l.level.err {
		return
	}

	golog.Errorf(l.prefix+" "+format, args...)
}

func (l *Logger) Fatal(args ...any) {
	if !l.level.fatal {
		os.Exit(1)
	}

	args = append([]any{l.prefix}, args...)
	golog.Fatal(args...)
}

func (l *Logger) Fatalf(format string, args ...any) {
	if !l.level.fatal {
		os.Exit(1)
	}

	golog.Fatalf(l.prefix+" "+format, args...)
}

type LogFactory struct {
	level logLevelEnabled
}

func NewLogFactory() *LogFactory {
	return &LogFactory{
		level: newLogLevelEnabled(golog.Default.Level),
	}
}

func (l *LogFactory) EngineLogger() lib.Logger {
	return &Logger{
		prefix: "[engine]",
		level:  l.level,
	}
}

func (l *LogFactory) WorkspaceLogger() pasta.Logger {
	return &Logger{
		prefix: "[workspace]",
		level:  l.level,
	}
}

func (l *LogFactory) NodeLogger(id uint64, class string) pasta.Logger {
	return &Logger{
		prefix: fmt.Sprintf("[%s %d]", class, id),
		level:  l.level,
	}
}
