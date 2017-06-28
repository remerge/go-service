package service

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	rp "runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Shopify/sarama"
	"github.com/bobziuchkovski/cue"
	"github.com/bobziuchkovski/cue/hosted"
	"github.com/gin-gonic/gin"
	metrics "github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/exp"
	"github.com/remerge/go-env"
	"github.com/remerge/go-tracker"
	"github.com/spf13/cobra"
	"github.com/tylerb/graceful"
)

// CodeVersion will be set to the package version or git ref of consumers of
// go-service by their build system.
var CodeVersion = "unknown"

// CodeBuild will be set to the build number and generator of consumers of
// go-service by their build system.
var CodeBuild = "unknown"

// Service is the base for implementing custom services based on go-service. It
// should be extended with custom command line options and state required for
// the service to function.
type Service struct {
	Name        string
	Description string
	Command     *cobra.Command

	Log     cue.Logger
	Rollbar hosted.Rollbar

	Tracker struct {
		tracker.Tracker
		Connect       string
		EventMetadata tracker.EventMetadata
	}

	Server struct {
		Port   int
		Engine *gin.Engine
		Server *graceful.Server

		ShutdownTimeout   time.Duration
		ConnectionTimeout time.Duration

		Debug struct {
			Port   int
			Engine *gin.Engine
			Server *graceful.Server
		}

		TLS struct {
			Port   int
			Cert   string
			Key    string
			Server *graceful.Server
		}
	}
}

// NewService returns an initialized Service instance with an HTTP server
// running on `port` and a debug server running on `port + 9`.
func NewService(name string, port int) *Service {
	service := &Service{}
	service.Name = name
	service.Log = cue.NewLogger(name)
	service.Command = service.buildCommand()
	service.Server.Port = port
	return service
}

// Execute starts cobras main loop for command line handling. If the cobra
// command returns an error, the process panics.
func (service *Service) Execute() {
	service.Panic(service.Command.Execute(), "failed to execute command")
}

func (service *Service) buildCommand() *cobra.Command {
	cmd := &cobra.Command{}

	cmd.Use = service.Name
	cmd.Short = fmt.Sprintf("%s: %s", service.Name, service.Description)

	// global flags for all commands
	flags := cmd.PersistentFlags()

	debugP := flags.Bool(
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
		&service.Tracker.EventMetadata.Cluster,
		"cluster",
		"development",
		"cluster to run in (eu, us, etc)",
	)

	flags.StringVar(
		&service.Rollbar.Token,
		"rollbar-token",
		service.Rollbar.Token,
		"rollbar token",
	)

	// local service flags
	flags = cmd.Flags()

	flags.StringVar(
		&service.Tracker.Connect,
		"tracker-connect", "0.0.0.0:9092",
		"connect string for tracker",
	)

	flags.IntVar(
		&service.Server.Port,
		"server-port", service.Server.Port,
		"HTTP server port",
	)

	flags.DurationVar(
		&service.Server.ShutdownTimeout,
		"server-shutdown-timeout", 30*time.Second,
		"HTTP server shutdown timeout",
	)

	flags.DurationVar(
		&service.Server.ConnectionTimeout,
		"server-connection-timeout", 2*time.Minute,
		"HTTP connection idle timeout",
	)

	flags.IntVar(
		&service.Server.Debug.Port,
		"server-debug-port", 0,
		"HTTP debug server port (default server-port + 9)",
	)

	flags.IntVar(
		&service.Server.TLS.Port,
		"server-tls-port", 0,
		"HTTPS server port",
	)

	flags.StringVar(
		&service.Server.TLS.Cert,
		"server-tls-cert", "",
		"HTTPS server certificate",
	)

	flags.StringVar(
		&service.Server.TLS.Key,
		"server-tls-key", "",
		"HTTPS server certificate key",
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
		// reset env
		env.Set(env.Env)
		setLogFormat(*debugP)

		// configure rollbar
		if env.IsProd() {
			service.Rollbar.Environment = env.Env
			service.Rollbar.ProjectVersion = CodeVersion
			cue.CollectAsync(cue.WARN, 1024*1024, service.Rollbar.New())
			cue.SetFrames(1, 32)
		}

		// configure tracker
		service.Tracker.EventMetadata.Service = service.Name
		service.Tracker.EventMetadata.Environment = env.Env
		service.Tracker.EventMetadata.Host = getFQDN()
		service.Tracker.EventMetadata.Release = CodeVersion

		sarama.Logger = &saramaLoggerWrapper{
			logger: cue.NewLogger("sarama"),
		}

		// use all cores by default
		if os.Getenv("GOMAXPROCS") == "" {
			runtime.GOMAXPROCS(runtime.NumCPU())
		}

		// initialize gin engine
		gin.SetMode("release")
		service.Server.Engine = gin.New()
		service.Server.Engine.Use(
			ginRecovery(service.Name),
			ginLogger(service.Name),
		)
	}

	return cmd
}

// Run starts background workers for `go-tracker`, `go-metrics` and the debug
// server. The main HTTP interface is not started by default. Use `Serve` or
// `ServeTLS` in your service implementation.
func (service *Service) Run() {
	service.Log.WithFields(cue.Fields{
		"env":     service.Tracker.EventMetadata.Environment,
		"cluster": service.Tracker.EventMetadata.Cluster,
		"host":    service.Tracker.EventMetadata.Host,
		"release": CodeVersion,
		"build":   CodeBuild,
	}).Infof("starting %s", service.Tracker.EventMetadata.Service)

	// create cache folder if missing #nosec
	service.Panic(os.MkdirAll("cache", 0755), "failed to create cache folder")

	// check if we have been killed by a panic
	_, err := os.Stat("cache/.started")
	if err == nil {
		_, err = os.Stat("cache/.shutdown_done")
		if err != nil {
			// unclean shutdown
			service.Log.Warn("found unclean service shutdown")
		}
	}

	os.Remove("cache/.shutdown_done")
	os.Create("cache/.started")

	// start kafka tracker
	service.Tracker.Tracker, err = tracker.NewKafkaTracker(
		strings.Split(service.Tracker.Connect, ","),
		&service.Tracker.EventMetadata,
	)
	service.Panic(err, "failed to start tracker")

	// background jobs for go-metrics
	go service.flushMetrics(10 * time.Second)

	// start debug server
	if service.Server.Debug.Port < 1 && service.Server.Port > 0 {
		service.Server.Debug.Port = service.Server.Port + 9
	}

	if service.Server.Debug.Port > 0 {
		go service.serveDebug(service.Server.Debug.Port)
	}
}

// Serve starts a plain HTTP server on `service.Server.Port`. If `handler` is
// nil `service.Server.Engine` is used.
func (service *Service) Serve(handler http.Handler) {
	if handler == nil {
		handler = service.Server.Engine
	}

	service.Server.Server = &graceful.Server{
		Timeout:          service.Server.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", service.Server.Port),
		},
	}

	service.Server.Server.ReadTimeout = service.Server.ConnectionTimeout
	service.Server.Server.WriteTimeout = service.Server.ConnectionTimeout

	service.Log.WithFields(cue.Fields{
		"listen": service.Server.Server.Addr,
	}).Info("start server")

	service.Panic(service.Server.Server.ListenAndServe(), "server failed")
}

// ServeTLS starts a TLS encrypted HTTPS server on `service.Server.TLS.Port`.
// TLS support is disabled by default and needs to be configured with proper
// certificates in `service.Server.TLS.Key` and `service.Server.TLS.Cert`.
func (service *Service) ServeTLS(handler http.Handler) {
	if handler == nil {
		handler = service.Server.Engine
	}

	service.Server.TLS.Server = &graceful.Server{
		Timeout: service.Server.ShutdownTimeout,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", service.Server.TLS.Port),
		},
		NoSignalHandling: true,
	}

	service.Server.TLS.Server.ReadTimeout = service.Server.ConnectionTimeout
	service.Server.TLS.Server.WriteTimeout = service.Server.ConnectionTimeout

	service.Log.WithFields(cue.Fields{
		"listen": service.Server.TLS.Server.Server.Addr,
	}).Info("start tls server")

	service.Panic(
		service.Server.TLS.Server.ListenAndServeTLS(
			service.Server.TLS.Cert,
			service.Server.TLS.Key,
		), "tls server failed",
	)
}

func (service *Service) serveDebug(port int) {
	if service.Server.Debug.Engine == nil {
		service.Server.Debug.Engine = gin.New()
		service.Server.Debug.Engine.Use(
			ginRecovery(service.Name),
			ginLogger(service.Name),
		)
	}

	// expvar & go-metrics
	service.Server.Debug.Engine.GET("/vars",
		gin.WrapH(exp.ExpHandler(metrics.DefaultRegistry)))
	service.Server.Debug.Engine.GET("/metrics",
		gin.WrapH(exp.ExpHandler(metrics.DefaultRegistry)))

	// wrap pprof in gin
	service.Server.Debug.Engine.GET("/pprof/",
		gin.WrapF(pprof.Index))
	service.Server.Debug.Engine.GET("/pprof/block",
		gin.WrapH(pprof.Handler("block")))
	service.Server.Debug.Engine.GET("/pprof/cmdline",
		gin.WrapF(pprof.Cmdline))
	service.Server.Debug.Engine.GET("/pprof/goroutine",
		gin.WrapH(pprof.Handler("goroutine")))
	service.Server.Debug.Engine.GET("/pprof/heap",
		gin.WrapH(pprof.Handler("heap")))
	service.Server.Debug.Engine.GET("/pprof/profile",
		gin.WrapF(pprof.Profile))
	service.Server.Debug.Engine.GET("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	service.Server.Debug.Engine.POST("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	service.Server.Debug.Engine.GET("/pprof/threadcreate",
		gin.WrapH(pprof.Handler("threadcreate")))
	service.Server.Debug.Engine.GET("/pprof/trace",
		gin.WrapF(pprof.Trace))

	service.Server.Debug.Engine.GET("/blockprof/:rate", func(c *gin.Context) {
		r, err := strconv.Atoi(c.Param("rate"))
		if err != nil {
			c.Error(err)
			return
		}
		runtime.SetBlockProfileRate(r)
		c.String(http.StatusOK, "new rate %d", r)
	})

	service.Server.Debug.Engine.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	service.Server.Debug.Server = &graceful.Server{
		Timeout:          service.Server.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: service.Server.Debug.Engine,
			Addr:    fmt.Sprintf(":%d", port),
		},
	}

	service.Log.WithFields(cue.Fields{
		"listen": service.Server.Debug.Server.Server.Addr,
	}).Info("start debug server")

	service.Panic(service.Server.Debug.Server.ListenAndServe(),
		"debug server failed")
}

// ShutdownServers gracefully shuts down the all running HTTP servers (plain,
// TLS, debug) and waits for connections to close until
// `service.Server.ShutdownTimeout` is reached.
func (service *Service) ShutdownServers() {
	var serverChan, tlsServerChan, debugServerChan <-chan struct{}

	if service.Server.TLS.Server != nil {
		service.Log.Info("tls server shutdown")
		tlsServerChan = service.Server.TLS.Server.StopChan()
		service.Server.TLS.Server.Stop(service.Server.ShutdownTimeout)
	}

	if service.Server.Server != nil {
		service.Log.Info("server shutdown")
		serverChan = service.Server.Server.StopChan()
		service.Server.Server.Stop(service.Server.ShutdownTimeout)
	}

	if service.Server.Debug.Server != nil {
		service.Log.Info("debug server shutdown")
		debugServerChan = service.Server.Debug.Server.StopChan()
		service.Server.Debug.Server.Stop(service.Server.ShutdownTimeout)
	}

	if service.Server.TLS.Server != nil {
		<-tlsServerChan
		service.Log.Info("tls server shutdown complete")
		service.Server.TLS.Server = nil
	}

	if service.Server.Server != nil {
		<-serverChan
		service.Log.Info("server shutdown complete")
		service.Server.Server = nil
	}

	if service.Server.Debug.Server != nil {
		<-debugServerChan
		service.Log.Info("debug server shutdown complete")
		service.Server.Debug.Server = nil
	}
}

// Shutdown shuts down all HTTP servers (see `ShutdownServers`), the tracker
// and flushes all log and error buffers.
func (service *Service) Shutdown() {
	service.Log.Info("service shutdown")

	service.ShutdownServers()

	if service.Tracker.Tracker != nil {
		service.Log.Info("tracker shutdown")
		service.Tracker.Tracker.Close()
	}

	service.Log.Info("shutdown done")
	os.Create("cache/.shutdown_done")

	// flush cue buffers
	cue.Close(5 * time.Second)
}

// Panic reports cause to our logger and panics. If cause is nil Panic does
// nothing.
func (service *Service) Panic(cause interface{}, msg string) {
	if cause == nil {
		return
	}
	service.Log.ReportRecovery(cause, msg)
	cue.Close(5 * time.Second)
	panic(cause)
}

// Wait registers signal handlers for SIGHUP, SIGINT, SIGQUIT and SIGTERM and
// shuts down the service on notification.
func (service *Service) Wait(shutdownCallback func()) syscall.Signal {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT,
		syscall.SIGQUIT, syscall.SIGTERM)
	for {
		sig := <-ch
		service.Log.WithFields(cue.Fields{
			"signal": sig.String(),
		}).Info("shutdown")
		go service.shutdownCheck()
		shutdownCallback()
		return sig.(syscall.Signal)
	}
}

func (service *Service) shutdownCheck() {
	time.Sleep(1 * time.Minute)
	service.Log.Warn("shutdown blocked")
	_ = rp.Lookup("goroutine").WriteTo(os.Stdout, 1)
	go service.shutdownCheck()
}
