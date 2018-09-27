package service

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	metrics "github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/exp"
	"github.com/spf13/cobra"
	"github.com/tylerb/graceful"

	"github.com/remerge/cue"
	"github.com/remerge/go-service/registry"
)

type server struct {
	Name   string
	Port   int
	Engine *gin.Engine
	Server *graceful.Server

	log             cue.Logger
	metricsRegistry metrics.Registry
	promMetrics     *PrometheusMetrics

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

type serverConfig struct {
	Port int
}

type serverParams struct {
	registry.Params
	serverConfig    `registry:"lazy"`
	Log             cue.Logger
	Cmd             *cobra.Command
	metricsRegistry metrics.Registry
	// promMetrics     *PrometheusMetrics
}

func registerServer(r *registry.ServiceRegistry, name string) {
	r.Register(func(p *serverParams) (*server, error) {
		f := &server{
			Port:            p.Port,
			log:             p.Log,
			metricsRegistry: p.metricsRegistry,
			Name:            name,
			// promMetrics:     p.promMetrics,
		}
		f.configureFlags(p.Cmd)
		return f, nil
	})
}

func (s *server) configureFlags(cmd *cobra.Command) {
	flags := cmd.Flags()

	flags.IntVar(
		&s.Port,
		"server-port", s.Port,
		"HTTP server port",
	)

	flags.DurationVar(
		&s.ShutdownTimeout,
		"server-shutdown-timeout", 30*time.Second,
		"HTTP server shutdown timeout",
	)

	flags.DurationVar(
		&s.ConnectionTimeout,
		"server-connection-timeout", 2*time.Minute,
		"HTTP connection idle timeout",
	)

	flags.IntVar(
		&s.Debug.Port,
		"server-debug-port", 0,
		"HTTP debug server port (default server-port + 9)",
	)

	flags.IntVar(
		&s.TLS.Port,
		"server-tls-port", 0,
		"HTTPS server port",
	)

	flags.StringVar(
		&s.TLS.Cert,
		"server-tls-cert", "",
		"HTTPS server certificate",
	)

	flags.StringVar(
		&s.TLS.Key,
		"server-tls-key", "",
		"HTTPS server certificate key",
	)
}

func (e *Executor) WithServer(port int) *Executor {
	err := e.ServiceRegistry.Request(&e.Server, serverConfig{Port: port})
	if err != nil {
		panic(err)
	}
	e.services = append(e.services, e.Server)
	return e
}

func (s *server) Init() error {
	gin.SetMode("release")
	s.Engine = gin.New()
	s.Engine.Use(
		ginRecovery(s.Name),
		ginLogger(s.Name),
	)
	return nil
}

func (s *server) Run() error {
	// start debug server
	if s.Debug.Port < 1 && s.Port > 0 {
		s.Debug.Port = s.Port + 9
	}

	if s.Debug.Port > 0 {
		s.initDebugEngine()
		go s.serveDebug(s.Debug.Port)
	}

	return nil
}

func (s *server) Shutdown(os.Signal) {
	var serverChan, tlsServerChan, debugServerChan <-chan struct{}

	if s.TLS.Server != nil {
		s.log.Info("tls server shutdown")
		tlsServerChan = s.TLS.Server.StopChan()
		s.TLS.Server.Stop(s.ShutdownTimeout)
	}

	if s.Server != nil {
		s.log.Info("server shutdown")
		serverChan = s.Server.StopChan()
		s.Server.Stop(s.ShutdownTimeout)
	}

	if s.Debug.Server != nil {
		s.log.Info("debug server shutdown")
		debugServerChan = s.Debug.Server.StopChan()
		s.Debug.Server.Stop(s.ShutdownTimeout)
	}

	if s.TLS.Server != nil {
		<-tlsServerChan
		s.log.Info("tls server shutdown complete")
		s.TLS.Server = nil
	}

	if s.Server != nil {
		<-serverChan
		s.log.Info("server shutdown complete")
		s.Server = nil
	}

	if s.Debug.Server != nil {
		<-debugServerChan
		s.log.Info("debug server shutdown complete")
		s.Debug.Server = nil
	}

}

func (s *server) initDebugEngine() {
	if s.Debug.Engine == nil {
		s.Debug.Engine = gin.New()
		s.Debug.Engine.Use(
			ginRecovery(s.Name),
			ginLogger(s.Name),
		)
	}
}

func (s *server) serveDebug(port int) {
	// expvar & go-metrics
	s.Debug.Engine.GET("/vars",
		gin.WrapH(exp.ExpHandler(s.metricsRegistry)))

	// wrap pprof in gin
	s.Debug.Engine.GET("/pprof/",
		gin.WrapF(pprof.Index))
	s.Debug.Engine.GET("/pprof/block",
		gin.WrapH(pprof.Handler("block")))
	s.Debug.Engine.GET("/pprof/cmdline",
		gin.WrapF(pprof.Cmdline))
	s.Debug.Engine.GET("/pprof/goroutine",
		gin.WrapH(pprof.Handler("goroutine")))
	s.Debug.Engine.GET("/pprof/heap",
		gin.WrapH(pprof.Handler("heap")))
	s.Debug.Engine.GET("/pprof/profile",
		gin.WrapF(pprof.Profile))
	s.Debug.Engine.GET("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Debug.Engine.POST("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Debug.Engine.GET("/pprof/threadcreate",
		gin.WrapH(pprof.Handler("threadcreate")))
	s.Debug.Engine.GET("/pprof/trace",
		gin.WrapF(pprof.Trace))

	s.Debug.Engine.GET("/blockprof/:rate", func(c *gin.Context) {
		r, err := strconv.Atoi(c.Param("rate"))
		if err != nil {
			_ = c.Error(err)
			return
		}
		runtime.SetBlockProfileRate(r)
		c.String(http.StatusOK, "new rate %d", r)
	})

	s.Debug.Engine.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	s.Debug.Engine.GET("/metrics", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4")
		c.String(http.StatusOK, s.promMetrics.String())
	})

	s.Debug.Server = &graceful.Server{
		Timeout:          s.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: s.Debug.Engine,
			Addr:    fmt.Sprintf(":%d", port),
		},
	}

	s.log.WithFields(cue.Fields{
		"listen": s.Debug.Server.Server.Addr,
	}).Info("start debug server")

	s.log.Panic(s.Debug.Server.ListenAndServe(),
		"debug server failed")
}

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
