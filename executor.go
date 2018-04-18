package service

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Shopify/sarama"
	"github.com/remerge/cue"
	"github.com/remerge/cue/hosted"
	env "github.com/remerge/go-env"
	"github.com/spf13/cobra"
)

// Executor is the base for implementing custom services based on go-service. It
// should be extended with custom command line options and state required for
// the service to function.
type Executor struct {
	service Service

	Name        string
	Description string
	Command     *cobra.Command

	Log           *Logger
	Rollbar       hosted.Rollbar
	StatsDAddress string

	Debug struct {
		Active bool
	}

	Tracker *tracker
	Server  *server
}

// NewExecutor creates new basic executor
func NewExecutor(name string, service Service) *Executor {
	s := &Executor{}
	s.service = service
	s.Name = name
	s.Log = NewLogger(name)
	s.Command = s.buildCommand()
	return s
}

func (s *Executor) run() error {
	err := s.runExtended()
	if err != nil {
		return err
	}
	return s.service.Run()
}

func (s *Executor) init() error {
	env.Set(env.Env)
	setLogFormat(s.Debug.Active)

	s.Log.Info("Start initialization...")

	// configure rollbar
	if env.IsProd() {
		s.Rollbar.Environment = env.Env
		s.Rollbar.ProjectVersion = CodeVersion
		cue.CollectAsync(cue.ERROR, 1024*1024, s.Rollbar.New())
		cue.SetFrames(1, 32)
	}

	sarama.Logger = &saramaLoggerWrapper{
		logger: cue.NewLogger("sarama"),
	}

	// use all cores by default
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	// background jobs for go-metrics
	if env.IsProd() {
		go s.flushMetrics(10 * time.Second)
	}

	// create cache folder if missing #nosec
	err := os.MkdirAll("cache", 0755)
	if err != nil {
		return fmt.Errorf("failed to create cache folder. %v", err)
	}

	// check if we have been killed by a panic
	_, err = os.Stat("cache/.started")
	if err == nil {
		_, err = os.Stat("cache/.shutdown_done")
		if err != nil {
			// unclean shutdown
			s.Log.Warn("found unclean service shutdown")
		}
	}

	_ = os.Remove("cache/.shutdown_done")

	_, err = os.Create("cache/.started")
	if err != nil {
		return fmt.Errorf("failed to create cache/.started. %v", err)
	}
	err = s.initExtended()
	if err != nil {
		return err
	}
	return s.service.Init()
}

// Execute starts cobras main loop for command line handling. If the cobra
// command returns an error, the process panics.
func (s *Executor) Execute() {
	s.Log.Panic(s.Command.Execute(), "failed to execute command")
}

func (s *Executor) buildCommand() *cobra.Command {
	cmd := &cobra.Command{}

	cmd.Use = s.Name
	cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)

	// global flags for all commands
	flags := cmd.PersistentFlags()

	flags.BoolVar(
		&s.Debug.Active,
		"debug",
		false,
		"enable debug logging",
	)

	flags.StringVar(
		&env.Env,
		"environment",
		env.Env,
		"environment to run in (development, test, production)",
	)

	flags.StringVar(
		&s.Rollbar.Token,
		"rollbar-token",
		s.Rollbar.Token,
		"rollbar token",
	)

	flags.StringVar(
		&s.StatsDAddress,
		"statsd-address",
		"127.0.0.1:8092",
		"host:port for the statsd daemon",
	)

	// version command for deployment
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "display version and exit",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(CodeVersion)
		},
	})

	cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		err := s.init()
		if err != nil {
			s.Log.Panic(err, "Error during service init")
		}
	}
	cmd.Run = func(cmd *cobra.Command, args []string) {
		done := make(chan bool)

		go func() {
			err := s.run()
			if err != nil {
				s.Log.Panic(err, "Error during service run")
			}
			done <- true
		}()

		waitForShutdown(s.shutdown, done)
	}

	return cmd
}

// Shutdown shuts down all HTTP servers (see `ShutdownServers`), the tracker
// and flushes all log and error buffers.
func (s *Executor) shutdown(sig os.Signal) {
	v := "none (normal termination)"
	if sig != nil {
		v = sig.String()
	}
	s.Log.WithValue("signal", v).Info("service shutdown")

	s.Log.Info("shutdown done")
	_, _ = os.Create("cache/.shutdown_done")

	// flush cue buffers
	_ = cue.Close(5 * time.Second)

	s.extendedShutdown(sig)
	s.service.Shutdown(sig)
}
