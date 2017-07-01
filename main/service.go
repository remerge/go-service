package main

import (
	"github.com/bobziuchkovski/cue"
	"github.com/remerge/go-service"
	"github.com/spf13/cobra"
)

var log = cue.NewLogger("main")

func main() {
	s := service.NewService("service", 9990)

	s.Command.Run = func(cmd *cobra.Command, args []string) {
		go s.Run()
		s.Wait(s.Shutdown)
	}

	s.Execute()
}
