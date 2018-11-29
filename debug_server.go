package service

import (
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

	"github.com/remerge/cue"
	"github.com/remerge/go-service/registry"
)

type debugServer struct {
	*server
	metricsRegistry metrics.Registry
	promMetrics     *PrometheusMetrics
}

type debugServerParams struct {
	registry.Params
	ServerConfig    `registry:"lazy"`
	Log             cue.Logger
	Cmd             *cobra.Command
	MetricsRegistry metrics.Registry
	PromMetrics     *PrometheusMetrics
}

type DebugEngine struct {
	*gin.Engine
}

func registerDebugServer(r Registry, name string) {
	r.Register(func(p *debugServerParams) (*debugServer, error) {
		f := &debugServer{
			server: &server{
				Name:              name,
				Port:              p.Port,
				log:               p.Log,
				ShutdownTimeout:   30 * time.Second,
				ConnectionTimeout: 5 * time.Minute,
			},
			metricsRegistry: p.MetricsRegistry,
			promMetrics:     p.PromMetrics,
		}
		f.configureFlags(p.Cmd)
		return f, nil
	})
	r.Register(func(s *debugServer) (*DebugEngine, error) {
		return &DebugEngine{s.Engine}, nil
	})
}

func (s *debugServer) configureFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.IntVar(
		&s.Port,
		"server-debug-port", s.Port,
		"HTTP debug server port",
	)

}

func (s *debugServer) Init() error {
	if err := s.server.Init(); err != nil {
		return err
	}
	go s.serveDebug()
	return nil
}

func (s *debugServer) Shutdown(sig os.Signal) {
	s.log.Info("shutdown debug server")
	s.server.Shutdown(sig)
}

func (s *debugServer) serveDebug() {
	// expvar & go-metrics
	s.Engine.GET("/vars",
		gin.WrapH(exp.ExpHandler(s.metricsRegistry)))

	// wrap pprof in gin
	s.Engine.GET("/pprof/",
		gin.WrapF(pprof.Index))
	s.Engine.GET("/pprof/block",
		gin.WrapH(pprof.Handler("block")))
	s.Engine.GET("/pprof/cmdline",
		gin.WrapF(pprof.Cmdline))
	s.Engine.GET("/pprof/goroutine",
		gin.WrapH(pprof.Handler("goroutine")))
	s.Engine.GET("/pprof/heap",
		gin.WrapH(pprof.Handler("heap")))
	s.Engine.GET("/pprof/profile",
		gin.WrapF(pprof.Profile))
	s.Engine.GET("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Engine.POST("/pprof/symbol",
		gin.WrapF(pprof.Symbol))
	s.Engine.GET("/pprof/threadcreate",
		gin.WrapH(pprof.Handler("threadcreate")))
	s.Engine.GET("/pprof/trace",
		gin.WrapF(pprof.Trace))

	s.Engine.GET("/blockprof/:rate", func(c *gin.Context) {
		r, err := strconv.Atoi(c.Param("rate"))
		if err != nil {
			_ = c.Error(err)
			return
		}
		runtime.SetBlockProfileRate(r)
		c.String(http.StatusOK, "new rate %d", r)
	})

	s.Engine.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	s.Engine.GET("/metrics", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4")
		c.String(http.StatusOK, s.promMetrics.String())
	})

	s.log.WithFields(cue.Fields{
		"port": s.Port,
	}).Info("start debug server")

	s.Serve(nil)
}
