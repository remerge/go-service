package service

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Shopify/sarama"
	metrics "github.com/rcrowley/go-metrics"
	"github.com/remerge/cue"
	"github.com/remerge/cue/hosted"
	env "github.com/remerge/go-env"
	lft "github.com/remerge/go-lock_free_timer"
	"github.com/spf13/cobra"
)

// Base provides common main service functionallity and can be embeded in a main service object.
// It provides
// - a metrics registry (using our lock free implementation)
// - a tracker to send message to kafka (if requested)
// - a HTTP server (if requested)
// - a debug server (if requested, serves prometeus metrics)
// - a debug forwarder (if requested)
// - a rollbar instance (sends logged Errors to rollbar in production mode)
//
// On startup Base checks if the previous shutdown was graceful.
type Base struct {
	Name        string
	Description string

	Log     *Logger
	Rollbar hosted.Rollbar

	Tracker *Tracker
	Server  *Server

	DebugServer   *debugServer
	debugForwader *debugForwader
	stackdriver   *stackdriver

	metricsRegistry metrics.Registry
	promMetrics     *PrometheusMetrics
}

// RegisterBase registers a Base ctor with a given DI registry. Additonal it registers
// the ctors for logger, metrics, debug forwarder, tracker, http server and debug http server.
func RegisterBase(r Registry, name string) {
	r.Register(func(cmd *cobra.Command) (*Base, error) {
		metricsRegistry := lft.DefaultRegistry
		base := &Base{
			Name:            name,
			Log:             NewLogger(name),
			metricsRegistry: metricsRegistry,
			promMetrics:     NewPrometheusMetrics(metricsRegistry, name),
		}

		base.configureFlags(cmd)

		r.Register(func() (cue.Logger, error) {
			return base.Log, nil
		})

		r.Register(func() (metrics.Registry, error) {
			return base.metricsRegistry, nil
		})

		r.Register(func() (*PrometheusMetrics, error) {
			return base.promMetrics, nil
		})

		registerDebugForwarder(r)
		registerTracker(r, name)
		registerServer(r, name)
		registerDebugServer(r, name)
		registerStackdriver(r, name)

		return base, nil
	})
}

func (b *Base) configureFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&b.Rollbar.Token,
		"rollbar-token",
		b.Rollbar.Token,
		"rollbar token",
	)
}

func (b *Base) Init() error {
	b.Log.Info("Start initialization...")

	// configure rollbar
	if env.IsProd() {
		b.Rollbar.Environment = env.Env
		b.Rollbar.ProjectVersion = CodeVersion
		cue.CollectAsync(cue.ERROR, 1024*1024, b.Rollbar.New())
		cue.SetFrames(1, 32)
	}

	sarama.Logger = &saramaLoggerWrapper{
		logger: NewLogger("sarama"),
	}

	// use all cores by default
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	// flush prom metrics every 10s
	if env.IsProd() {
		go b.flushMetrics(10 * time.Second)
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
			b.Log.Warn("found unclean service shutdown")
		}
	}

	_ = os.Remove("cache/.shutdown_done")

	_, err = os.Create("cache/.started")
	if err != nil {
		return fmt.Errorf("failed to create cache/.started. %v", err)
	}
	return nil
}

// Shutdown shuts down all HTTP servers (see `ShutdownServers`), the tracker
// and flushes all log and error buffers.
func (b *Base) Shutdown(sig os.Signal) {
	v := "none (normal termination)"
	if sig != nil {
		v = sig.String()
	}
	b.Log.WithValue("signal", v).Info("service shutdown")
	_, err := os.Create("cache/.shutdown_done")
	if err != nil {
		_ = b.Log.Errorf(err, "Error creating shutdown file")
	}

	b.Log.Info("shutdown done")
}

// // WithMetricsRegistry replaces default metrics registry.
// // This method should be called ONCE BEFORE adding other services to the Runner with WithXYZ or direct service registry request
// func (e *Runner) WithMetricsRegistry(r metrics.Registry) *Runner {
// 	e.metricsRegistry = r
// 	return e
// }

// CreateTracker creates a tracker object for this Base
func (b *Base) CreateTracker(r *RunnerWithRegistry) {
	r.Create(&b.Tracker)
}

// CreateServer creates a server object for this Base listening on a given port
func (b *Base) CreateServer(r *RunnerWithRegistry, port int) {
	r.Create(&b.Server, ServerConfig{Port: port})
}

// CreateDebugServer creates a debug server object for this Base listening on a given port
func (b *Base) CreateDebugServer(r *RunnerWithRegistry, defaultPort int) {
	r.Create(&b.DebugServer, ServerConfig{Port: defaultPort})
}

// CreateDebugForwarder creates a debug forwarder for this Base listening on a given port
func (b *Base) CreateDebugForwarder(r *RunnerWithRegistry, port int) {
	r.Create(&b.debugForwader, DebugForwaderConfig{Port: port})
}

// CreateStackdriver create a stackdriver service
func (b *Base) CreateStackdriver(r *RunnerWithRegistry) {
	r.Create(&b.stackdriver)
}

// ForwardToDebugConns forwards data to connected debug listeners
func (b *Base) ForwardToDebugConns(data []byte) {
	b.debugForwader.forward(data)
}

// HasOpenDebugForwardingConns checks if there are open connections  to debug listeners
func (b *Base) HasOpenDebugForwardingConns() bool {
	return b.debugForwader.hasOpenConnections()
}

func SafeCreate(ctor func(...interface{}) (interface{}, error), err error) interface{} {
	if err != nil {
		panic(err)
	}
	r, err := ctor()
	if err != nil {
		panic(err)
	}
	return r
}
