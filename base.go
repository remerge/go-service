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
// - a health checker (if requested)
// - a rollbar instance (sends logged Errors to rollbar in production mode)
// - a stackdriver connection to colelct ongoing profiles (if requested)
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
	HealthChecker *HealthChecker

	metricsRegistry *lft.Registry
	promMetrics     *PrometheusMetrics
	closeChannel    chan struct{}
}

// RegisterBase registers a Base ctor with a given DI registry. Additonal it registers
// the ctors for logger, metrics, debug forwarder, tracker, http server, stackdriver and debug http server.
func RegisterBase(r Registry, name string) {
	r.Register(func(cmd *cobra.Command) (*Base, error) {

		metricsRegistry := lft.DefaultRegistry
		base := &Base{
			Name:            name,
			Log:             NewLogger(name),
			metricsRegistry: metricsRegistry,
			promMetrics:     NewPrometheusMetrics(metricsRegistry, name),
			closeChannel:    make(chan struct{}),
		}

		// until we correctly register metrics with the correct registry everywhere, sync
		go func() {
			for range time.NewTicker(time.Second * 30).C {
				lft.DefaultRegistry.PullFrom(metrics.DefaultRegistry)
			}
		}()

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

		r.Register(NewDefaultHealthCheckerService)
		r.Register(NewTrackerService, name)
		r.Register(newStackdriverService, name)
		r.Register(newDebugForwader)
		registerServer(r, name)
		registerDebugServer(r, name)

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
	go b.runMetricsFlusher(10*time.Second, b.closeChannel)

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

	// stop metrics - in theory we need to wait for them ... maybe we should make a service out of them as well
	close(b.closeChannel)

	_, err := os.Create("cache/.shutdown_done")
	if err != nil {
		_ = b.Log.Errorf(err, "Error creating shutdown file")
	}

	b.Log.Info("shutdown done")
}

// UseTracker creates a tracker object for this Base and registers it as a service to be run
func (b *Base) UseTracker(r *RunnerWithRegistry) {
	r.RequestAndSet(&b.Tracker)
}

// UseStackdriver create a stackdriver service and registers it as a service to be run
func (b *Base) UseStackdriver(r *RunnerWithRegistry) {
	r.RequestAndSet(&b.stackdriver)
}

// CreateHealthChecker create a HealthChecker and registers it as a service to be run
func (b *Base) UseHealthChecker(r *RunnerWithRegistry) {
	r.RequestAndSet(&b.HealthChecker)
}

// CreateServer creates a server object for this Base and configures the default port and
// registers it as a service to be run
func (b *Base) CreateServer(r *RunnerWithRegistry, defaultPort int) {
	r.Create(&b.Server, ServerConfig{Port: defaultPort})
}

// CreateDebugServer creates a debug server object for this Base and configures the default port and
// registers it as a service to be run
func (b *Base) CreateDebugServer(r *RunnerWithRegistry, defaultPort int) {
	r.Create(&b.DebugServer, ServerConfig{Port: defaultPort})
}

// CreateDebugForwarder creates a debug forwarder for this Base and configures the default port and
// registers it as a service to be run
func (b *Base) CreateDebugForwarder(r *RunnerWithRegistry, defaultPort int) {
	r.Create(&b.debugForwader, DebugForwaderConfig{Port: defaultPort})
}

// ForwardToDebugConns forwards data to connected debug listeners
func (b *Base) ForwardToDebugConns(data []byte) {
	b.debugForwader.forward(data)
}

// HasOpenDebugForwardingConns checks if there are open connections  to debug listeners
func (b *Base) HasOpenDebugForwardingConns() bool {
	return b.debugForwader.hasOpenConnections()
}

func MustCreate(ctor func(...interface{}) (interface{}, error), err error) interface{} {
	if err != nil {
		panic(err)
	}
	r, err := ctor()
	if err != nil {
		panic(err)
	}
	return r
}
