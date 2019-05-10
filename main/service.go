package main

import (
	"os"

	"github.com/remerge/go-service"
)

// SimpleService simple service without server
type SimpleService struct {
	*service.Executor
}

func newSimpleService() *SimpleService {
	s := &SimpleService{}
	s.Executor = service.NewExecutor("service", s)
	return s
}

func (s *SimpleService) Init() error {
	s.Log.Info("Initializing...")
	return nil
}

func (s *SimpleService) Run() error {
	s.Log.Info("Running...")
	return nil
}

func (s *SimpleService) Shutdown(_ os.Signal) {
	s.Log.Info("Shutdown...")
}

func main() {
	s := newSimpleService()
	//s := newSimpleServer()

	s.Execute()
}
