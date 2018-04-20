package service

import (
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
}

func newTestService() *testService {
	s := &testService{}
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
	return nil
}

func (s *testService) Shutdown(sig os.Signal) {
	s.shutdownCalled = true
}

func TestExecutionWithoutReadyChannel(t *testing.T) {
	subject := newTestService()
	subject.Execute()
	require.True(t, subject.initCalled)
	require.True(t, subject.runCalled)
	require.True(t, subject.shutdownCalled)
}

func TestExecutionWithReadyChannel(t *testing.T) {
	subject := newTestService()
	go subject.Execute()
	_, ok := <-subject.Ready()
	require.True(t, ok)
	require.True(t, subject.initCalled)
	require.False(t, subject.runCalled)
	require.False(t, subject.shutdownCalled)
}
