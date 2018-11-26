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
	"github.com/remerge/go-service/registry"
	"github.com/spf13/cobra"
)

// Base provides common functionality for a main service
// TODO: complete
// by default
// - metric.Registry
// - logger
// - prom metrics in production
// - debug forwarder
// - debbug server
type Base struct {
	Name        string
	Description string

	Log     *Logger
	Rollbar hosted.Rollbar

	Tracker *tracker
	Server  *server

	*debugServer
	*debugForwader

	metricsRegistry metrics.Registry
	promMetrics     *PrometheusMetrics

	Debug struct {
		Active bool
	}
}

func RegisterBase(r *registry.Registry, name string) {
	r.Register(func(cmd *cobra.Command) (*Base, error) {
		metricsRegistry := metrics.DefaultRegistry
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

		return base, nil
	})
}

func (b *Base) configureFlags(cmd *cobra.Command) {
	flags := cmd.Flags()

	flags.BoolVar(
		&b.Debug.Active,
		"debug",
		false,
		"enable debug logging",
	)

	flags.StringVar(
		&b.Rollbar.Token,
		"rollbar-token",
		b.Rollbar.Token,
		"rollbar token",
	)
}

func (b *Base) Init() error {
	env.Set(env.Env)
	// TODO: we should do this a bit earlier
	// setLogFormat(b.Debug.Active)

	b.Log.Info("Start initialization...")

	// configure rollbar
	if env.IsProd() {
		b.Rollbar.Environment = env.Env
		b.Rollbar.ProjectVersion = CodeVersion
		cue.CollectAsync(cue.ERROR, 1024*1024, b.Rollbar.New())
		cue.SetFrames(1, 32)
	}

	sarama.Logger = &saramaLoggerWrapper{
		logger: cue.NewLogger("sarama"),
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

func (b *Base) CreateTracker(r *RunnerWithRegistry, port int) {
	r.Create(&b.Tracker)
}

func (b *Base) CreateServer(r *RunnerWithRegistry, port int) {
	r.Create(&b.Server, ServerConfig{Port: port})
}

func (b *Base) CreateDebugServer(r *RunnerWithRegistry, defaultPort int) {
	r.Create(&b.debugServer, ServerConfig{Port: defaultPort})
}

func (b *Base) CreateDebugForwarder(r *RunnerWithRegistry, port int) {
	r.Create(&b.debugForwader, DebugForwaderConfig{Port: port})
}

func (b *Base) ForwardToDebugConns(data []byte) {
	b.debugForwader.forward(data)
}

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
