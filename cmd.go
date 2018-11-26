package service

import (
	"fmt"

	env "github.com/remerge/go-env"
	"github.com/spf13/cobra"
)

type InitFnc func(*RunnerWithRegistry)

// TODO: add description
func Cmd(name string, initFnc InitFnc) *cobra.Command {
	// TODO: remove setLogFormat
	setLogFormat(true)
	cmd := &cobra.Command{}

	cmd.Use = name
	// cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)
	// cmd.Use = s.Name
	// cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)

	// global flags for all commands
	flags := cmd.PersistentFlags()

	flags.StringVar(
		&env.Env,
		"environment",
		env.Env,
		"environment to run in (development, test, production)",
	)

	// version command for deployment
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "display version and exit",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(CodeVersion)
		},
	})

	// TODO: should we always do this?
	r := NewRunnerWithRegistry()
	r.Register(func() (*cobra.Command, error) {
		return cmd, nil
	})
	RegisterBase(r.Registry, name)
	initFnc(r)

	cmd.Run = func(cmd *cobra.Command, args []string) {
		r.Run()
	}

	return cmd
}
