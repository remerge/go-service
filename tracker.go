package service

import (
	"fmt"
	"os"
	"strings"

	env "github.com/remerge/go-env"
	"github.com/remerge/go-tools/fqdn"
	gotracker "github.com/remerge/go-tracker"

	"github.com/spf13/cobra"

	"github.com/remerge/cue"
)

type Tracker struct {
	gotracker.Tracker
	Name          string
	Connect       string
	EventMetadata gotracker.EventMetadata
	log           cue.Logger
}

func NewTracker(log cue.Logger, cmd *cobra.Command, name string) (*Tracker, error) {
	t := &Tracker{
		log:  log,
		Name: name,
	}
	t.configureFlags(cmd)
	return t, nil
}

func NewTrackerService(r *RunnerWithRegistry, log cue.Logger, cmd *cobra.Command, name string) (*Tracker, error) {
	t, err := NewTracker(log, cmd, name)
	if err != nil {
		return nil, err
	}
	r.Add(t)
	return t, nil

}

func (t *Tracker) configureFlags(cmd *cobra.Command) {
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

func (t *Tracker) Init() error {
	t.EventMetadata.Service = t.Name
	t.EventMetadata.Environment = env.Env
	t.EventMetadata.Host = fqdn.Get()
	t.EventMetadata.Release = CodeVersion

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

func (t *Tracker) Shutdown(os.Signal) {
	if t != nil && t.Tracker != nil {
		t.log.Info("tracker shutdown")
		t.Tracker.Close()
	}
}

// These methods provide compliance with userdb.ServiceTracker interface
func (t *Tracker) GetTracker() gotracker.Tracker {
	return t.Tracker
}

func (t *Tracker) GetConnect() string {
	return t.Connect
}

func (t *Tracker) GetEventMetadata() gotracker.EventMetadata {
	return t.EventMetadata
}
