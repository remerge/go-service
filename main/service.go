package main

import (
	"github.com/bobziuchkovski/cue"
	"github.com/remerge/go-service"
	"github.com/spf13/cobra"
)

var log = cue.NewLogger("main")

func main() {
	service := service.NewService("service", 9990)

	service.Command.Run = func(cmd *cobra.Command, args []string) {
		go service.Run()
		service.Wait(service.Shutdown)
	}

	service.Execute()
}
