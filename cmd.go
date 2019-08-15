package service

import (
	"fmt"
	"io/ioutil"
	"os"

	env "github.com/remerge/go-env"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type InitFnc func(*RunnerWithRegistry)

var logLevelString string

// Cmd wraps a init function with service setup code
// - create a service registry and a runner
// - registered all Base services
// - creates the base cobra Cmd
func Cmd(name string, initFnc InitFnc) *cobra.Command {
	initLogCollector()
	setLogLevelFrom(parseLogLevelFlat())

	cmd := &cobra.Command{}
	cmd.Use = name
	// cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)
	// cmd.Use = s.Name
	// cmd.Short = fmt.Sprintf("%s: %s", s.Name, s.Description)

	// global flags for all commands
	flags := cmd.PersistentFlags()
	addLogFlag(flags, &logLevelString)

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

	r := NewRunnerWithRegistry()
	// so services can register themselves for execution
	r.Register(func() (*RunnerWithRegistry, error) {
		return r, nil
	})
	r.Register(func() (*cobra.Command, error) {
		return cmd, nil
	})
	RegisterBase(r.Registry, name)
	initFnc(r)

	cmd.SilenceUsage = true
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return r.Run()
	}

	return cmd
}

func parseLogLevelFlat() (level string) {
	fs := pflag.NewFlagSet("log", pflag.ContinueOnError)
	addLogFlag(fs, &level)
	fs.SetOutput(ioutil.Discard)
	fs.Parse(os.Args[1:])
	return level
}

// Add a log level flag to a given FlagSet
func addLogFlag(fs *pflag.FlagSet, target *string) {
	fs.StringVarP(
		target,
		"log-level",
		"l",
		"info",
		"log level (debug,info,warn,error,fatal,off)",
	)
}
