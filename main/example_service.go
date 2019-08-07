package main

import (
	"fmt"
	"os"

	"github.com/remerge/go-service"
	"github.com/spf13/cobra"
)

type ExampleService struct {
	*service.Base
}

func (s *ExampleService) Init() error {
	s.Log.Info("Initializing...")
	return nil
}

func (s *ExampleService) Shutdown(os.Signal) {
	s.Log.Info("Shutdown...")
}

var rootCmd = service.Cmd("example", Init)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func Init(r *service.RunnerWithRegistry) {
	// without any requirements:
	// s := &ExampleService{}
	s := service.MustCreate(r.Register(func(cmd *cobra.Command) (*ExampleService, error) {
		return &ExampleService{}, nil
	})).(*ExampleService)

	r.Create(&s.Base)
	s.CreateDebugServer(r, 4008)
	r.Create(&s)
}

func main() {
	Execute()
}
