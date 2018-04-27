package service

import (
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type lockingService struct {
	*Executor
	cond          *sync.Cond
	runDone       bool
	shutdownCount int
	t             *testing.T
}

func newLockingService(t *testing.T) *lockingService {
	s := &lockingService{}
	s.t = t
	mutex := sync.Mutex{}
	mutex.Lock()
	s.cond = sync.NewCond(&mutex)
	s.Executor = NewExecutor("locking_service", s)
	return s
}

func (s *lockingService) Init() error {
	return nil
}

func (s *lockingService) Run() error {
	time.Sleep(time.Millisecond * 100)
	s.cond.Wait()
	s.runDone = true
	return nil
}

func (s *lockingService) Shutdown(sig os.Signal) {
	s.cond.Broadcast()
	s.shutdownCount++
	time.Sleep(time.Second)
}

func TestSignalShutdown(t *testing.T) {
	subject := newLockingService(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		subject.Execute()
	}()
	time.Sleep(time.Second)
	signalChannel <- syscall.SIGKILL
	wg.Wait()
	require.True(t, subject.runDone)
	require.Equal(t, 1, subject.shutdownCount)
}
