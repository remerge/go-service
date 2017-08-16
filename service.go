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

	Log           *Logger
	Rollbar       hosted.Rollbar
	StatsDAddress string

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
	s := &Service{}
	s.Name = name
	s.Log = NewLogger(name)
	s.Command = s.buildCommand()
	s.Server.Port = port
	return s
}

// Execute starts cobras main loop for command line handling. If the cobra
// command returns an error, the process panics.
func (s *Service) Execute() {
	s.Log.Panic(s.Command.Execute(), "failed to execute command")
}

func (s *Service) buildCommand() *cobra.Command {
	cmd := &cobra.Command{}

	cmd.Use = s.Name
	cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)

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
		&s.Tracker.EventMetadata.Cluster,
		"cluster",
		"development",
		"cluster to run in (eu, us, etc)",
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

	// local service flags
	flags = cmd.Flags()

	flags.StringVar(
		&s.Tracker.Connect,
		"tracker-connect", "0.0.0.0:9092",
		"connect string for tracker",
	)

	flags.IntVar(
		&s.Server.Port,
		"server-port", s.Server.Port,
		"HTTP server port",
	)

	flags.DurationVar(
		&s.Server.ShutdownTimeout,
		"server-shutdown-timeout", 30*time.Second,
		"HTTP server shutdown timeout",
	)

	flags.DurationVar(
		&s.Server.ConnectionTimeout,
		"server-connection-timeout", 2*time.Minute,
		"HTTP connection idle timeout",
	)

	flags.IntVar(
		&s.Server.Debug.Port,
		"server-debug-port", 0,
		"HTTP debug server port (default server-port + 9)",
	)

	flags.IntVar(
		&s.Server.TLS.Port,
		"server-tls-port", 0,
		"HTTPS server port",
	)

	flags.StringVar(
		&s.Server.TLS.Cert,
		"server-tls-cert", "",
		"HTTPS server certificate",
	)

	flags.StringVar(
		&s.Server.TLS.Key,
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
			s.Rollbar.Environment = env.Env
			s.Rollbar.ProjectVersion = CodeVersion
			cue.CollectAsync(cue.WARN, 1024*1024, s.Rollbar.New())
			cue.SetFrames(1, 32)
		}

		// configure tracker
		s.Tracker.EventMetadata.Service = s.Name
		s.Tracker.EventMetadata.Environment = env.Env
		s.Tracker.EventMetadata.Host = GetFQDN()
		s.Tracker.EventMetadata.Release = CodeVersion

		sarama.Logger = &saramaLoggerWrapper{
			logger: cue.NewLogger("sarama"),
		}

		// use all cores by default
		if os.Getenv("GOMAXPROCS") == "" {
			runtime.GOMAXPROCS(runtime.NumCPU())
		}

		// initialize gin engine
		gin.SetMode("release")
		s.Server.Engine = gin.New()
		s.Server.Engine.Use(
			ginRecovery(s.Name),
			ginLogger(s.Name),
		)
	}

	return cmd
}

// Run starts background workers for `go-tracker`, `go-metrics` and the debug
// server. The main HTTP interface is not started by default. Use `Serve` or
// `ServeTLS` in your service implementation.
func (s *Service) Run() {
	s.Log.WithFields(cue.Fields{
		"env":     s.Tracker.EventMetadata.Environment,
		"cluster": s.Tracker.EventMetadata.Cluster,
		"host":    s.Tracker.EventMetadata.Host,
		"release": CodeVersion,
		"build":   CodeBuild,
	}).Infof("starting %s", s.Tracker.EventMetadata.Service)

	// create cache folder if missing #nosec
	s.Log.Panic(os.MkdirAll("cache", 0755), "failed to create cache folder")

	// check if we have been killed by a panic
	_, err := os.Stat("cache/.started")
	if err == nil {
		_, err = os.Stat("cache/.shutdown_done")
		if err != nil {
			// unclean shutdown
			s.Log.Warn("found unclean service shutdown")
		}
	}

	_ = os.Remove("cache/.shutdown_done")

	_, err = os.Create("cache/.started")
	s.Log.Panic(err, "failed to create cache/.started")

	// start kafka tracker
	s.Tracker.Tracker, err = tracker.NewKafkaTracker(
		strings.Split(s.Tracker.Connect, ","),
		&s.Tracker.EventMetadata,
	)
	s.Log.Panic(err, "failed to start tracker")

	// background jobs for go-metrics
	if env.IsProd() {
		go s.flushMetrics(10 * time.Second)
	}

	// start debug server
	if s.Server.Debug.Port < 1 && s.Server.Port > 0 {
		s.Server.Debug.Port = s.Server.Port + 9
	}

	if s.Server.Debug.Port > 0 {
		go s.serveDebug(s.Server.Debug.Port)
	}
}

// Serve starts a plain HTTP server on `service.Server.Port`. If `handler` is
// nil `service.Server.Engine` is used.
func (s *Service) Serve(handler http.Handler) {
	if handler == nil {
		handler = s.Server.Engine
	}

	s.Server.Server = &graceful.Server{
		Timeout:          s.Server.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", s.Server.Port),
		},
	}

	s.Server.Server.ReadTimeout = s.Server.ConnectionTimeout
	s.Server.Server.WriteTimeout = s.Server.ConnectionTimeout

	s.Log.WithFields(cue.Fields{
		"listen": s.Server.Server.Addr,
	}).Info("start server")

	s.Log.Panic(s.Server.Server.ListenAndServe(), "server failed")
}

// ServeTLS starts a TLS encrypted HTTPS server on `service.Server.TLS.Port`.
// TLS support is disabled by default and needs to be configured with proper
// certificates in `service.Server.TLS.Key` and `service.Server.TLS.Cert`.
func (s *Service) ServeTLS(handler http.Handler) {
	if handler == nil {
		handler = s.Server.Engine
	}

	s.Server.TLS.Server = &graceful.Server{
		Timeout: s.Server.ShutdownTimeout,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", s.Server.TLS.Port),
		},
		NoSignalHandling: true,
	}

	s.Server.TLS.Server.ReadTimeout = s.Server.ConnectionTimeout
	s.Server.TLS.Server.WriteTimeout = s.Server.ConnectionTimeout

	s.Log.WithFields(cue.Fields{
		"listen": s.Server.TLS.Server.Server.Addr,
	}).Info("start tls server")

	s.Log.Panic(
		s.Server.TLS.Server.ListenAndServeTLS(
			s.Server.TLS.Cert,
			s.Server.TLS.Key,
		), "tls server failed",
	)
}

func (s *Service) serveDebug(port int) {
	if s.Server.Debug.Engine == nil {
		s.Server.Debug.Engine = gin.New()
		s.Server.Debug.Engine.Use(
			ginRecovery(s.Name),
			ginLogger(s.Name),
		)
	}

	// expvar & go-metrics
	s.Server.Debug.Engine.GET("/vars",
		gin.WrapH(exp.ExpHandler(metrics.DefaultRegistry)))
	s.Server.Debug.Engine.GET("/metrics",
		gin.WrapH(exp.ExpHandler(metrics.DefaultRegistry)))

	// wrap pprof in gin
	s.Server.Debug.Engine.GET("/pprof/",
		gin.WrapF(pprof.Index))
	s.Server.Debug.Engine.GET("/pprof/block",
		gin.WrapH(pprof.Handler("block")))
	s.Server.Debug.Engine.GET("/pprof/cmdline",
		gin.WrapF(pprof.Cmdline))
	s.Server.Debug.Engine.GET("/pprof/goroutine",
		gin.WrapH(pprof.Handler("goroutine")))
	s.Server.Debug.Engine.GET("/pprof/heap",
		gin.WrapH(pprof.Handler("heap")))
	s.Server.Debug.Engine.GET("/pprof/profile",
		gin.WrapF(pprof.Profile))
	s.Server.Debug.Engine.GET("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Server.Debug.Engine.POST("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Server.Debug.Engine.GET("/pprof/threadcreate",
		gin.WrapH(pprof.Handler("threadcreate")))
	s.Server.Debug.Engine.GET("/pprof/trace",
		gin.WrapF(pprof.Trace))

	s.Server.Debug.Engine.GET("/blockprof/:rate", func(c *gin.Context) {
		r, err := strconv.Atoi(c.Param("rate"))
		if err != nil {
			_ = c.Error(err)
			return
		}
		runtime.SetBlockProfileRate(r)
		c.String(http.StatusOK, "new rate %d", r)
	})

	s.Server.Debug.Engine.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	s.Server.Debug.Server = &graceful.Server{
		Timeout:          s.Server.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: s.Server.Debug.Engine,
			Addr:    fmt.Sprintf(":%d", port),
		},
	}

	s.Log.WithFields(cue.Fields{
		"listen": s.Server.Debug.Server.Server.Addr,
	}).Info("start debug server")

	s.Log.Panic(s.Server.Debug.Server.ListenAndServe(),
		"debug server failed")
}

// ShutdownServers gracefully shuts down the all running HTTP servers (plain,
// TLS, debug) and waits for connections to close until
// `service.Server.ShutdownTimeout` is reached.
func (s *Service) ShutdownServers() {
	var serverChan, tlsServerChan, debugServerChan <-chan struct{}

	if s.Server.TLS.Server != nil {
		s.Log.Info("tls server shutdown")
		tlsServerChan = s.Server.TLS.Server.StopChan()
		s.Server.TLS.Server.Stop(s.Server.ShutdownTimeout)
	}

	if s.Server.Server != nil {
		s.Log.Info("server shutdown")
		serverChan = s.Server.Server.StopChan()
		s.Server.Server.Stop(s.Server.ShutdownTimeout)
	}

	if s.Server.Debug.Server != nil {
		s.Log.Info("debug server shutdown")
		debugServerChan = s.Server.Debug.Server.StopChan()
		s.Server.Debug.Server.Stop(s.Server.ShutdownTimeout)
	}

	if s.Server.TLS.Server != nil {
		<-tlsServerChan
		s.Log.Info("tls server shutdown complete")
		s.Server.TLS.Server = nil
	}

	if s.Server.Server != nil {
		<-serverChan
		s.Log.Info("server shutdown complete")
		s.Server.Server = nil
	}

	if s.Server.Debug.Server != nil {
		<-debugServerChan
		s.Log.Info("debug server shutdown complete")
		s.Server.Debug.Server = nil
	}
}

// Shutdown shuts down all HTTP servers (see `ShutdownServers`), the tracker
// and flushes all log and error buffers.
func (s *Service) Shutdown() {
	s.Log.Info("service shutdown")

	s.ShutdownServers()

	if s.Tracker.Tracker != nil {
		s.Log.Info("tracker shutdown")
		s.Tracker.Tracker.Close()
	}

	s.Log.Info("shutdown done")
	_, _ = os.Create("cache/.shutdown_done")

	// flush cue buffers
	_ = cue.Close(5 * time.Second)
}

// Wait registers signal handlers for SIGHUP, SIGINT, SIGQUIT and SIGTERM and
// shuts down the service on notification.
func (s *Service) Wait(shutdownCallback func()) syscall.Signal {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT,
		syscall.SIGQUIT, syscall.SIGTERM)
	for {
		sig := <-ch
		s.Log.WithFields(cue.Fields{
			"signal": sig.String(),
		}).Info("shutdown")
		go s.shutdownCheck(5)
		shutdownCallback()
		return sig.(syscall.Signal)
	}
}

func (s *Service) shutdownCheck(i int) {
	// do not recurse forever
	if i < 1 {
		return
	}

	time.Sleep(1 * time.Minute)
	s.Log.Warn("shutdown blocked")
	_ = rp.Lookup("goroutine").WriteTo(os.Stdout, 1)
	go s.shutdownCheck(i - 1)
}
