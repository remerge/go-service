package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/remerge/cue"
	"github.com/remerge/cue/collector"
	"github.com/remerge/cue/format"
	env "github.com/remerge/go-env"
)

var termCollector cue.Collector

func setLogLevel(l cue.Level) {
	cue.SetLevel(l, termCollector)
}

func setLogLevelFrom(l string) {
	var lvl cue.Level
	switch strings.ToLower(l) {
	case "debug":
		lvl = cue.DEBUG
	case "info":
		lvl = cue.INFO
	case "warn":
		lvl = cue.WARN
	case "error":
		lvl = cue.ERROR
	case "fatal":
		lvl = cue.FATAL
	case "off":
		lvl = cue.OFF
	}
	setLogLevel(lvl)
}

func initLogCollector() {
	formatter := format.Formatf(
		"%v [%v:%v] %v",
		format.Level,
		format.ContextName,
		format.SourceWithLine,
		format.HumanMessage,
	)

	if !env.IsProd() {
		formatter = format.Colorize(
			format.Formatf(
				"%v %v",
				format.Time(time.RFC3339),
				formatter,
			),
		)
	}

	termCollector = collector.Terminal{
		Formatter: formatter,
	}.New()

	cue.Collect(cue.INFO, termCollector)
}

type saramaLoggerWrapper struct {
	logger cue.Logger
}

func (slw *saramaLoggerWrapper) Print(v ...interface{}) {
	slw.logger.Info(fmt.Sprint(v...))
}

func (slw *saramaLoggerWrapper) Printf(format string, v ...interface{}) {
	slw.logger.Info(fmt.Sprintf(format, v...))
}

func (slw *saramaLoggerWrapper) Println(v ...interface{}) {
	slw.logger.Info(fmt.Sprintln(v...))
}
