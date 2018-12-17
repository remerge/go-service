package service

import (
	"os"

	"cloud.google.com/go/profiler"
)

func (e *Executor) WithStackDriver() *Executor {
	flags := e.Command.Flags()
	flags.BoolVar(
		&e.enableStackdriver,
		"enable-stackdriver", e.enableStackdriver,
		"Enable stackdriver",
	)
	return e
}

func (e *Executor) initStackdriver() {
	if !e.enableStackdriver {
		return
	}
	keyPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if keyPath == "" {
		e.Log.Warn("could not start stackdriver profiler: env variable GOOGLE_APPLICATION_CREDENTIALS is empty")
		return
	}

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		e.Log.Warnf("could not start stackdriver profiler: keyfile does not exist %v", keyPath)
		return
	}

	e.Log.Info("starting stackdriver profiler")

	if err := profiler.Start(profiler.Config{
		Service:        e.Name,
		ServiceVersion: CodeVersion,
		ProjectID:      "stackdriver-profiler-test",
	}); err != nil {
		e.Log.Warnf("could not start stackdriver profiler: %v", err)
	}
}
