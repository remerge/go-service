package service

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Shopify/sarama"
	"github.com/rcrowley/go-metrics"
	"github.com/remerge/cue"
	"github.com/remerge/cue/hosted"
	env "github.com/remerge/go-env"
	"github.com/remerge/go-service/registry"
	"github.com/spf13/cobra"
)

// CodeVersion will be set to the package version or git ref of consumers of
// go-service by their build system.
var CodeVersion = "unknown"

// CodeBuild will be set to the build number and generator of consumers of
// go-service by their build system.
var CodeBuild = "unknown"

// Executor is the base for implementing custom services based on go-service. It
// should be extended with custom command line options and state required for
// the service to function.
type Executor struct {
	service Service

	*registry.ServiceRegistry

	// Sends nil when inited worked correctly, or error otherwize
	// You can use it to be notified the end of init
	readyC chan struct{}
	stopC  chan struct{}
	doneC  chan struct{}

	Name        string
	Description string
	Command     *cobra.Command

	Log     *Logger
	Rollbar hosted.Rollbar

	Tracker *tracker
	Server  *server

	*debugForwader

	metricsRegistry metrics.Registry
	promMetrics     *PrometheusMetrics

	doneClosed int32
	Debug      struct {
		Active bool
	}

	services []Service
}

// NewExecutor creates new basic executor
func NewExecutor(name string, service Service) *Executor {
	e := &Executor{
		service:         service,
		Name:            name,
		Log:             NewLogger(name),
		readyC:          make(chan struct{}, 1),
		stopC:           make(chan struct{}),
		doneC:           make(chan struct{}),
		metricsRegistry: metrics.DefaultRegistry,
		ServiceRegistry: registry.New(),
	}

	e.Command = e.buildCommand()

	e.Register(func() (*cobra.Command, error) {
		return e.Command, nil
	})

	e.Register(func() (cue.Logger, error) {
		return e.Log, nil
	})

	e.Register(func() (metrics.Registry, error) {
		return e.metricsRegistry, nil
	})

	e.Register(func() (*PrometheusMetrics, error) {
		return e.promMetrics, nil
	})

	registerDebugForwarder(e.ServiceRegistry)
	registerTracker(e.ServiceRegistry, name)
	registerServer(e.ServiceRegistry, name)

	return e
}

// StopChan gets the stop channel which will block until
// stopping has completed, at which point it is closed.
// Callers should never close the stop channel.
func (s *Executor) StopChan() <-chan struct{} {
	return s.stopC
}

func (s *Executor) WaitForShutdown() {
	<-s.stopC
}

func (s *Executor) run() error {
	for _, service := range s.services {
		if err := service.Run(); err != nil {
			return err
		}
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
	s.promMetrics = NewPrometheusMetrics(s.metricsRegistry, s.Name)
	if env.IsProd() {
		go s.flushMetrics(10 * time.Second)
	}

	// TODO: pass these around properly and don't set them directly
	if s.Server != nil {
		s.Server.promMetrics = s.promMetrics
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

	for _, service := range s.services {
		if err := service.Init(); err != nil {
			return err
		}
	}

	return s.service.Init()
}

// Ready returns channel that signals that service is inited
func (s *Executor) Ready() <-chan struct{} {
	return s.readyC
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

	// version command for deployment
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "display version and exit",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(CodeVersion)
		},
	})

	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.init()
			s.readyC <- struct{}{}
			if err != nil {
				s.Log.Panic(err, "Error during service init")
			}
		}()
		err := waitTimeout(&wg, time.Minute*5)
		if err != nil {
			s.Log.Panic(err, "Error during service init")
		}
	}
	cmd.Run = func(cmd *cobra.Command, args []string) {
		go func() {
			err := s.run()
			if err != nil {
				_ = s.Log.Error(err, "Error during service run")
			}
			s.Stop()
		}()

		waitForShutdown(s.Log, s.shutdown, s.doneC)
	}

	return cmd
}

// nolint: unparam
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) error {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("Timeout after %v", timeout)
	}
}

// Stop stops the executor and forces shutdown
// Exits only when the service is stopped
func (s *Executor) Stop() {
	if atomic.CompareAndSwapInt32(&s.doneClosed, 0, 1) {
		close(s.doneC)
	}
	s.WaitForShutdown()
}

// Shutdown shuts down all HTTP servers (see `ShutdownServers`), the tracker
// and flushes all log and error buffers.
func (s *Executor) shutdown(sig os.Signal) {
	s.service.Shutdown(sig)

	// shutdown contained services
	for i := len(s.services); i >= 0; i-- {
		s.services[i].Shutdown(sig)
	}

	close(s.readyC)
	if atomic.CompareAndSwapInt32(&s.doneClosed, 0, 1) {
		close(s.doneC)
	}

	v := "none (normal termination)"
	if sig != nil {
		v = sig.String()
	}
	s.Log.WithValue("signal", v).Info("service shutdown")
	_, err := os.Create("cache/.shutdown_done")
	if err != nil {
		_ = s.Log.Errorf(err, "Error creating shutdown file")
	}

	// flush cue buffers
	_ = cue.Close(5 * time.Second)
	s.Log.Info("shutdown done")
	close(s.stopC)
}

func (e *Executor) RequestServices(services ...interface{}) {
	for _, s := range services {
		err := e.ServiceRegistry.Request(s)
		if err != nil {
			panic(err)
		}
		// s is a pointer to a pointer to a type instance implementing a Service
		// TODO: how to cast this without reflections? Am I stupid?
		sv := reflect.ValueOf(s).Elem().Interface().(Service)
		e.services = append(e.services, sv)
	}
}

// WithMetricsRegistry replaces default metrics registry.
// This method should be called ONCE BEFORE adding other services to the executor with WithXYZ or direct service registry request
func (e *Executor) WithMetricsRegistry(r metrics.Registry) *Executor {
	e.metricsRegistry = r
	return e
}
