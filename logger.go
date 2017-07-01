package service

import (
	"fmt"
	"time"

	"github.com/bobziuchkovski/cue"
)

type Logger struct {
	cue.Logger
}

func NewLogger(name string) *Logger {
	return &Logger{
		Logger: cue.NewLogger(name),
	}
}

func (l *Logger) Panic(cause interface{}, message string) {
	if cause == nil {
		return
	}
	l.ReportRecovery(cause, message)
	_ = cue.Close(5 * time.Second)
	panic(cause)
}

func (l *Logger) Panicf(cause interface{}, format string, values ...interface{}) {
	if cause == nil {
		return
	}
	l.ReportRecovery(cause, fmt.Sprintf(format, values...))
	_ = cue.Close(5 * time.Second)
	panic(cause)
}
