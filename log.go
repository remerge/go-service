package service

import (
	"fmt"
	"time"

	"github.com/remerge/cue"
	"github.com/remerge/cue/collector"
	"github.com/remerge/cue/format"
	env "github.com/remerge/go-env"
)

func setLogFormat(debug bool) {
	level := cue.INFO

	if debug {
		level = cue.DEBUG
	}

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

	cue.Collect(level, collector.Terminal{
		Formatter: formatter,
	}.New())
}

type saramaLoggerWrapper struct {
	logger cue.Logger
}

func (slw *saramaLoggerWrapper) Print(v ...interface{}) {
	slw.logger.Info(fmt.Sprint(v...))
}

func (slw *saramaLoggerWrapper) Printf(f string, v ...interface{}) {
	slw.logger.Info(fmt.Sprintf(f, v...))
}

func (slw *saramaLoggerWrapper) Println(v ...interface{}) {
	slw.logger.Info(fmt.Sprintln(v...))
}
