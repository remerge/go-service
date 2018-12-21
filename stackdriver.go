package service

import (
	"fmt"
	"os"

	"cloud.google.com/go/profiler"
	env "github.com/remerge/go-env"

	"github.com/spf13/cobra"

	"github.com/remerge/cue"
)

type stackdriver struct {
	log               cue.Logger
	enableStackdriver bool
	name              string
}

func registerStackdriver(r Registry, name string) {
	r.Register(func(log cue.Logger, cmd *cobra.Command) (*stackdriver, error) {
		s := &stackdriver{
			log:  log,
			name: name,
		}
		cmd.Flags().BoolVar(
			&s.enableStackdriver,
			"enable-stackdriver", s.enableStackdriver,
			"Enable stackdriver",
		)
		return s, nil
	})
}

func (s *stackdriver) Init() error {
	if !s.enableStackdriver {
		return nil
	}
	if !env.IsProd() {
		s.log.Info("stackdriver enabled but we are not running in production, disabling.")
		return nil
	}
	keyPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if keyPath == "" {
		return fmt.Errorf("could not start stackdriver profiler: env variable GOOGLE_APPLICATION_CREDENTIALS is empty")
	}

	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return fmt.Errorf("could not start stackdriver profiler: keyfile does not exist %v", keyPath)
	}

	s.log.Info("starting stackdriver profiler")

	if err := profiler.Start(profiler.Config{
		Service:        s.name,
		ServiceVersion: CodeVersion,
		ProjectID:      "stackdriver-profiler-test",
	}); err != nil {
		return fmt.Errorf("could not start stackdriver profiler: %v", err)
	}
	return nil
}

func (s *stackdriver) Shutdown(os.Signal) {}
