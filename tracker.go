package service

import (
	"fmt"
	"os"
	"strings"

	env "github.com/remerge/go-env"
	gotracker "github.com/remerge/go-tracker"

	"github.com/spf13/cobra"

	"github.com/remerge/cue"
	"github.com/remerge/go-service/registry"
)

type tracker struct {
	gotracker.Tracker
	Name          string
	Connect       string
	EventMetadata gotracker.EventMetadata
	log           cue.Logger
}

func registerTracker(r *registry.ServiceRegistry, name string) {
	r.Register(func(log cue.Logger, cmd *cobra.Command) (*tracker, error) {
		t := &tracker{
			log:  log,
			Name: name,
		}
		t.configureFlags(cmd)
		return t, nil
	})
}

func (t *tracker) configureFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(
		&t.EventMetadata.Cluster,
		"cluster",
		"development",
		"cluster to run in (eu, us, etc)",
	)
	cmd.Flags().StringVar(
		&t.Connect,
		"tracker-connect", "0.0.0.0:9092",
		"connect string for tracker",
	)
}

// WithTracker adds tracker to ExtendedExecutor.
// This method should be called ONCE BEFORE Execute() method.
func (e *Executor) WithTracker() *Executor {
	err := e.ServiceRegistry.Request(&e.Tracker)
	if err != nil {
		panic(err)
	}
	e.services = append(e.services, e.Tracker)
	return e
}

func (t *tracker) Init() error {
	t.EventMetadata.Service = t.Name
	t.EventMetadata.Environment = env.Env
	t.EventMetadata.Host = GetFQDN()
	t.EventMetadata.Release = CodeVersion
	return nil
}

func (t *tracker) Run() error {
	t.log.WithFields(cue.Fields{
		"env":     t.EventMetadata.Environment,
		"cluster": t.EventMetadata.Cluster,
		"host":    t.EventMetadata.Host,
		"release": CodeVersion,
		"build":   CodeBuild,
	}).Infof("starting %s", t.EventMetadata.Service)

	var err error
	t.Tracker, err = gotracker.NewKafkaTracker(
		strings.Split(t.Connect, ","),
		&t.EventMetadata,
	)
	if err != nil {
		return fmt.Errorf("failed to start tracker. %v", err)
	}
	return nil
}

func (t *tracker) Shutdown(os.Signal) {
	if t != nil && t.Tracker != nil {
		t.log.Info("tracker shutdown")
		t.Tracker.Close()
	}
}