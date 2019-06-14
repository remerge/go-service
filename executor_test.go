package service

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testService struct {
	*Executor
	initCalled     bool
	runCalled      bool
	shutdownCalled bool
	runError       error
}

func newTestService(runError error) *testService {
	s := &testService{}
	s.runError = runError
	s.Executor = NewExecutor("test_service", s)
	return s
}

func (s *testService) Init() error {
	s.initCalled = true
	return nil
}

func (s *testService) Run() error {
	time.Sleep(time.Second)
	s.runCalled = true
	return s.runError
}

func (s *testService) Shutdown(os.Signal) {
	s.shutdownCalled = true
}

func TestExecutionWithErrorOnRun(t *testing.T) {
	subject := newTestService(errors.New("Run error"))
	subject.Execute()
	require.True(t, subject.initCalled)
	require.True(t, subject.runCalled)
	require.True(t, subject.shutdownCalled)
}

func TestExecutionWithoutReadyChannel(t *testing.T) {
	subject := newTestService(nil)
	subject.Execute()
	require.True(t, subject.initCalled)
	require.True(t, subject.runCalled)
	require.True(t, subject.shutdownCalled)
}

func TestExecutionWithReadyChannel(t *testing.T) {
	subject := newTestService(nil)
	go subject.Execute()
	_, ok := <-subject.Ready()
	require.True(t, ok)
	require.True(t, subject.initCalled)
	require.False(t, subject.runCalled)
	require.False(t, subject.shutdownCalled)
}

func TestServiceNameWithSpace(t *testing.T) {
	subject := newTestService(nil)
	subject.Name = "name with space"
	require.Panics(t, subject.Execute)
}
