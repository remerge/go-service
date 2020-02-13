package service

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/tylerb/graceful"

	"github.com/remerge/cue"
	"github.com/remerge/go-service/registry"
)

type Server struct {
	Name string
	Port int

	Engine *gin.Engine
	Server *graceful.Server

	log cue.Logger

	ShutdownTimeout   time.Duration
	ConnectionTimeout time.Duration

	TLS struct {
		Port   int
		Cert   string
		Key    string
		Server *graceful.Server
	}

	requestsWg sync.WaitGroup
}

type ServerConfig struct {
	Port int
}

type serverParams struct {
	registry.Params
	ServerConfig `registry:"lazy"`
	Log          cue.Logger
	Cmd          *cobra.Command
}

func registerServer(r Registry, name string) {
	r.Register(func(p *serverParams) (*Server, error) {
		f := &Server{
			Port: p.Port,
			log:  p.Log,
			Name: name,
		}
		f.configureFlags(p.Cmd)
		return f, nil
	})
}

func (s *Server) configureFlags(cmd *cobra.Command) {
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

func (s *Server) Init() error {
	gin.SetMode("release")
	s.Engine = gin.New()
	s.Engine.Use(
		ginRequestsWaiter(&s.requestsWg),
		ginRecovery(s.Name),
		ginLogger(s.Name),
	)
	return nil
}

func (s *Server) Shutdown(os.Signal) {
	var serverChan, tlsServerChan <-chan struct{}

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

	allRequestsServedChan := make(chan struct{})
	go func() {
		s.requestsWg.Wait()
		close(allRequestsServedChan)
	}()
	select {
	case <-allRequestsServedChan:
		s.log.Info("all requests processed")
	case <-time.After(s.ShutdownTimeout):
		_ = s.log.Error(fmt.Errorf("shutdown timeout reached"), "remained unprocessed requests")
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
}

func (s *Server) Serve(handler http.Handler) {
	if handler == nil {
		handler = s.Engine
	}

	s.Server = &graceful.Server{
		Timeout:          s.ShutdownTimeout,
		NoSignalHandling: true,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", s.Port),
		},
	}

	s.Server.ReadTimeout = s.ConnectionTimeout
	s.Server.WriteTimeout = s.ConnectionTimeout

	s.log.WithFields(cue.Fields{
		"listen": s.Server.Addr,
	}).Info("start server")

	s.log.Panic(s.Server.ListenAndServe(), "server failed")
}

// ServeTLS starts a TLS encrypted HTTPS server on `service.Server.TLS.Port`.
// TLS support is disabled by default and needs to be configured with proper
// certificates in `service.Server.TLS.Key` and `service.Server.TLS.Cert`.
func (s *Server) ServeTLS(handler http.Handler) {
	if handler == nil {
		handler = s.Engine
	}

	s.TLS.Server = &graceful.Server{
		Timeout: s.ShutdownTimeout,
		Server: &http.Server{
			Handler: handler,
			Addr:    fmt.Sprintf(":%d", s.TLS.Port),
		},
		NoSignalHandling: true,
	}

	s.TLS.Server.ReadTimeout = s.ConnectionTimeout
	s.TLS.Server.WriteTimeout = s.ConnectionTimeout

	s.log.WithFields(cue.Fields{
		"listen": s.TLS.Server.Addr,
	}).Info("start tls server")

	s.log.Panic(
		s.TLS.Server.ListenAndServeTLS(
			s.TLS.Cert,
			s.TLS.Key,
		), "tls server failed",
	)
}
