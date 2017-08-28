package service

import (
	"fmt"
	"time"

	"github.com/remerge/cue"
)

// Logger is wrapper for cue.Logger with proper shutdown on panic
type Logger struct {
	cue.Logger
}

// NewLogger creates a new Logger
func NewLogger(name string) *Logger {
	return &Logger{
		Logger: cue.NewLogger(name),
	}
}

// Panic logs the given cause and message at the FATAL level and then
// calls panic(cause).  Panic does nothing if cause is nil.
func (l *Logger) Panic(cause interface{}, message string) {
	if cause == nil {
		return
	}
	l.ReportRecovery(cause, message)
	_ = cue.Close(5 * time.Second) // #nosec
	panic(cause)
}

// Panicf logs the given cause at the FATAL level using formatting rules
// from the fmt package and then calls panic(cause). Panicf does nothing if
// cause is nil.
func (l *Logger) Panicf(
	cause interface{},
	format string,
	values ...interface{},
) {
	if cause == nil {
		return
	}
	l.ReportRecovery(cause, fmt.Sprintf(format, values...))
	_ = cue.Close(5 * time.Second) // #nosec
	panic(cause)
}
