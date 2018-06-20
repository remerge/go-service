package service

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	metrics "github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/exp"
	"github.com/remerge/cue"
	env "github.com/remerge/go-env"
	gotracker "github.com/remerge/go-tracker"
	"github.com/tylerb/graceful"
)

// CodeVersion will be set to the package version or git ref of consumers of
// go-service by their build system.
var CodeVersion = "unknown"

// CodeBuild will be set to the build number and generator of consumers of
// go-service by their build system.
var CodeBuild = "unknown"

type tracker struct {
	gotracker.Tracker
	Connect       string
	EventMetadata gotracker.EventMetadata
}

type server struct {
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

// WithServer adds server to extended executor.
// This method should be called ONCE BEFORE Execute() method.
func (s *Executor) WithServer(port int) *Executor {
	s.Server = &server{Port: port}

	// local service flags
	flags := s.Command.Flags()

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
	return s
}

// WithTracker adds tracker to ExtendedExecutor.
// This method should be called ONCE BEFORE Execute() method.
func (s *Executor) WithTracker() *Executor {
	s.Tracker = &tracker{}
	flags := s.Command.PersistentFlags()

	flags.StringVar(
		&s.Tracker.EventMetadata.Cluster,
		"cluster",
		"development",
		"cluster to run in (eu, us, etc)",
	)

	flags = s.Command.Flags()

	flags.StringVar(
		&s.Tracker.Connect,
		"tracker-connect", "0.0.0.0:9092",
		"connect string for tracker",
	)
	return s
}

// WithMetricsRegistry replaces default metrics registry.
// This method should be called ONCE BEFORE Execute() method.
func (s *Executor) WithMetricsRegistry(r metrics.Registry) *Executor {
	s.metricsRegistry = r
	return s
}

func (s *Executor) initExtended() error {
	if s.Tracker != nil {
		s.Tracker.EventMetadata.Service = s.Name
		s.Tracker.EventMetadata.Environment = env.Env
		s.Tracker.EventMetadata.Host = GetFQDN()
		s.Tracker.EventMetadata.Release = CodeVersion
	}

	if s.Server != nil {
		// initialize gin engine
		gin.SetMode("release")
		s.Server.Engine = gin.New()
		s.Server.Engine.Use(
			ginRecovery(s.Name),
			ginLogger(s.Name),
		)
	}
	return nil
}

func (s *Executor) runExtended() error {
	if s.Tracker != nil {
		s.Log.WithFields(cue.Fields{
			"env":     s.Tracker.EventMetadata.Environment,
			"cluster": s.Tracker.EventMetadata.Cluster,
			"host":    s.Tracker.EventMetadata.Host,
			"release": CodeVersion,
			"build":   CodeBuild,
		}).Infof("starting %s", s.Tracker.EventMetadata.Service)

		var err error
		s.Tracker.Tracker, err = gotracker.NewKafkaTracker(
			strings.Split(s.Tracker.Connect, ","),
			&s.Tracker.EventMetadata,
		)
		if err != nil {
			return fmt.Errorf("failed to start tracker. %v", err)
		}
	}

	if s.Server != nil {
		// start debug server
		if s.Server.Debug.Port < 1 && s.Server.Port > 0 {
			s.Server.Debug.Port = s.Server.Port + 9
		}

		if s.Server.Debug.Port > 0 {
			s.initDebugEngine()
			go s.serveDebug(s.Server.Debug.Port)
		}
	}
	return nil
}

// Serve starts a plain HTTP server on `service.Server.Port`. If `handler` is
// nil `service.Server.Engine` is used.
func (s *Executor) Serve(handler http.Handler) {
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
func (s *Executor) ServeTLS(handler http.Handler) {
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

func (s *Executor) initDebugEngine() {
	if s.Server.Debug.Engine == nil {
		s.Server.Debug.Engine = gin.New()
		s.Server.Debug.Engine.Use(
			ginRecovery(s.Name),
			ginLogger(s.Name),
		)
	}
}

func (s *Executor) serveDebug(port int) {
	// expvar & go-metrics
	s.Server.Debug.Engine.GET("/vars",
		gin.WrapH(exp.ExpHandler(s.metricsRegistry)))

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

	s.Server.Debug.Engine.GET("/metrics", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4")
		c.String(http.StatusOK, s.promMetrics.String())
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
func (s *Executor) shutdownServers() {
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

func (s *Executor) extendedShutdown(os.Signal) {
	if s.Server != nil {
		s.shutdownServers()
	}

	if s.Tracker != nil && s.Tracker.Tracker != nil {
		s.Log.Info("tracker shutdown")
		s.Tracker.Tracker.Close()
	}
}
