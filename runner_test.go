package service

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testService struct {
	initRun         bool
	shutdownRun     bool
	sleepOnInit     time.Duration
	sleepOnShutdown time.Duration
	errOnInit       error
}

func (s *testService) Init() error {
	time.Sleep(s.sleepOnInit)
	s.initRun = true
	return s.errOnInit
}
func (s *testService) Shutdown(sig os.Signal) {
	time.Sleep(s.sleepOnShutdown)
	s.shutdownRun = true
}

func TestRunner(t *testing.T) {
	service := &testService{}
	r := NewRunner()
	var shutdownComplete bool
	r.PostShutdown = func(error) { shutdownComplete = true }
	r.Add(service)

	c := make(chan error)
	go func() { c <- r.Run() }()

	time.Sleep(1 * time.Millisecond)
	require.True(t, service.initRun)
	r.Stop()
	select {
	case err := <-c:
		require.NoError(t, err)
	case <-time.After(time.Millisecond):
		t.Error("Run did not terminate in time")
	}
	require.True(t, service.shutdownRun)
	require.True(t, shutdownComplete)
}
func TestRunnerErrorOnInit(t *testing.T) {
	service := &testService{errOnInit: errors.New("error on init")}
	r := NewRunner()
	r.Add(service)
	err := r.Run()
	require.Error(t, err)
	require.True(t, service.initRun)
	require.False(t, service.shutdownRun)
}

func TestRunnerTimeoutOnInit(t *testing.T) {
	service := &testService{sleepOnInit: 2 * time.Millisecond}
	config := NewRunnerDefaultConfig()
	config.InitTimeout = 1 * time.Millisecond
	r := NewRunnerWithConfig(config)
	r.Add(service)
	c := make(chan error)
	go func() {
		c <- r.Run()
	}()
	select {
	case err := <-c:
		require.Error(t, err)
		require.True(t, isTimeoutError(err))
	case <-time.After(2 * time.Millisecond):
		t.Error("Run did not terminate in time")
	}
	require.False(t, service.initRun)
	require.False(t, service.shutdownRun)
}
func TestRunnerTimeoutOnShutdown(t *testing.T) {
	service := &testService{sleepOnShutdown: 2 * time.Millisecond}
	config := NewRunnerDefaultConfig()
	config.ShutdownTimeout = 1 * time.Millisecond
	var timedOut *bool
	config.PostShutdown = func(err error) {
		te := isTimeoutError(err)
		timedOut = &te
	}
	r := NewRunnerWithConfig(config)
	r.Add(service)
	c := make(chan error)
	go func() {
		c <- r.Run()
	}()
	r.Stop()
	select {
	case err := <-c:
		require.Error(t, err)
		require.True(t, isTimeoutError(err))
	case <-time.After(3 * time.Millisecond):
		t.Error("Run did not terminate in time")
	}
	require.True(t, service.initRun)
	require.False(t, service.shutdownRun)
	require.NotNil(t, timedOut)
	require.True(t, *timedOut)
}
