package main

import (
	"net/http"
	"os"

	service "github.com/remerge/go-service"
)

// SimpleServer simple server example
type SimpleServer struct {
	*service.Executor
}

func newSimpleServer() *SimpleServer {
	s := &SimpleServer{}
	s.Executor = service.NewExecutor("service", s).WithServer(8080) //.WithTracker()
	return s
}

// Init make initialization
func (s *SimpleServer) Init() error {
	s.Log.Info("Initializing...")
	return nil
}

// Run make initialization
func (s *SimpleServer) Run() error {
	s.Log.Info("Running...")
	s.Serve(s)
	return nil
}

// ServeHTTP
func (s *SimpleServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	message := "Hello there"
	w.Write([]byte(message))
}

// Shutdown
func (s *SimpleServer) Shutdown(_ os.Signal) {
	s.Log.Info("Shutdown...")
}
