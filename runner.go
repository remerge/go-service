package service

import (
	"fmt"
	"os"
	"os/signal"
	"reflect"
	rp "runtime/pprof"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/remerge/cue"
)

// Runner runs services. Services that implement the Service interface can be added
// using the Add method. On Run they are started in the order of adding and Runner waits
// for a shutdown signal. The signal can come from the OS or Stop can be called. If such
// a signal is received the services are shutdown in reverse order. A timeout for service
// startup and shutdown can be configured using RunnerConfig. If a service doesn't terminate
// in time, the whole process is kill with a KILL signal.
type Runner struct {
	RunnerConfig
	services []*runnable
	signals  chan os.Signal
	log      cue.Logger
}

// RunnerConfig allows to configure timeouts for a Runner and provides a way to register a
// post shutdown callback.
type RunnerConfig struct {
	ShutdownTimeout     time.Duration
	InitTimeout         time.Duration
	OnInitSignalTimeout time.Duration
	PostShutdown        func(error)
}

type runnable struct {
	Service
	name string
}

// NewRunnerDefaultConfig create a default RunnerConfig
func NewRunnerDefaultConfig() RunnerConfig {
	return RunnerConfig{
		InitTimeout:         time.Minute,
		ShutdownTimeout:     time.Minute,
		OnInitSignalTimeout: 10 * time.Second,
		PostShutdown:        defaultPostShutdown,
	}
}

// NewRunner creates a Runner with a default config
func NewRunner() *Runner {
	return NewRunnerWithConfig(NewRunnerDefaultConfig())
}

// NewRunnerWithConfig creates a Runner with a provided config
func NewRunnerWithConfig(c RunnerConfig) *Runner {
	r := &Runner{
		signals:      make(chan os.Signal, 2), // this is buffered as the signal.Notify is using a non blocking send
		log:          NewLogger("runner"),
		RunnerConfig: c,
	}
	r.setupSignals()
	return r
}

// Add adds a service that should be run by the runner. The order in which services are added determines the start and shutdown order.
func (r *Runner) Add(s Service) {
	t := reflect.TypeOf(s)
	if t.Kind() == reflect.Interface {
		t = t.Elem()
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	r.services = append(r.services, &runnable{Service: s, name: t.String()})
}

// Run initializes all services added to this runner in the order  of adding. If a termination signal is received all services are
// shutdown in reverse order. If there was an error during initialization Run return this error early
func (r *Runner) Run() (err error) {
	var sig os.Signal
	var inited []*runnable

	defer func() {
		reversed := reverseServices(inited)

		r.log.Infof("shutting down services in order: %s", joinedServiceNames(reversed))

		shutdownErr := r.shutdownServices(reversed, sig)

		if r.PostShutdown != nil {
			r.PostShutdown(shutdownErr)
			_ = cue.Close(5 * time.Second)
		}

		if err == nil {
			err = shutdownErr
		}

	}()

	r.log.Infof("starting services in order: %s", joinedServiceNames(r.services))
	inited, sig, err = r.initServices()
	r.log.Infof("service start result err=%v signal=%v started=%s", err, sig, joinedServiceNames(inited))

	if err != nil {
		// if one service failed to init, we return and shutdown tthe inited
		return errors.Wrap(err, "error during startup")
	}

	if sig == nil {
		sig = <-r.signals
		r.log.Infof("signaled: %s", sig.String())
	}

	return err
}

// Stop signales this runner to initiate the shutdown process.
func (r *Runner) Stop() {
	r.signals <- syscall.SIGQUIT
}

func (r *Runner) setupSignals() {
	signal.Notify(r.signals,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)
}

func (r *Runner) initServices() ([]*runnable, os.Signal, error) {
	var inited []*runnable

	timer := time.NewTimer(r.InitTimeout)
	defer timer.Stop()
	c := make(chan error)
	for _, s := range r.services {
		t := time.Now()
		go func(s *runnable) {
			r.log.WithValue("service", s.name).Info("service begin init")
			c <- s.Init()
		}(s)
		select {
		case err := <-c:
			if err != nil {
				return inited, nil, errors.Wrapf(err, "service init failed for %s", s.name)
			}
			r.log.WithFields(cue.Fields{"service": s.name, "took": time.Now().Sub(t)}).Info("service init successful")
			inited = append(inited, s)
		case sig := <-r.signals:
			r.log.Infof("signaled: %s, waiting %v for %s to finish init before termination", sig.String(), r.OnInitSignalTimeout, s.name)
			extraTime := time.NewTimer(r.OnInitSignalTimeout)
			var err error
			select {
			case err = <-c:
				if err == nil {
					inited = append(inited, s)
				}
			case <-extraTime.C:
				r.log.Infof("signaled: %s, waiting for %s to finish init timed out, ignoring", sig.String(), s.name)
			}
			return inited, sig, err
		case <-timer.C:
			return inited, nil, newTimeoutError("timeout on service init", s.name, r.InitTimeout)
		}
	}
	return inited, nil, nil
}

// shutdownServices tries to shutdown every service/runnable owned by this runner in reverse order of initialization.
// It passes the given signal to the Shutdown method of the runnable. If the accumulated time it takes to shutdown
// all services is larger than the ShutdownTimeout, the shutdown is stopped and this methods returns a timeout error (as in timeout happend). Otherwise  nil is returned
func (r *Runner) shutdownServices(services []*runnable, sig os.Signal) error {
	timer := time.NewTimer(r.ShutdownTimeout)
	defer timer.Stop()

	c := make(chan struct{})
	for _, shuttingDown := range services {
		r.log.WithValue("service", shuttingDown.name).Info("shutting down")

		shutdownStarted := make(chan struct{})
		go func(s *runnable) {
			shutdownStarted <- struct{}{}
			t := time.Now()
			ticker := time.NewTicker(time.Second)
			go r.watchShutdown(ticker, shuttingDown)
			s.Shutdown(sig)
			ticker.Stop()
			r.log.WithFields(cue.Fields{"service": s.name, "took": time.Now().Sub(t)}).Info("shutdown done")
			c <- struct{}{}
		}(shuttingDown)

		<-shutdownStarted
		select {
		case <-c: // nothing needs to be done
		case <-timer.C:
			err := newTimeoutError("timeout on service shutdown", shuttingDown.name, r.ShutdownTimeout).logTo(r.log)
			shuttingDown = nil
			return err
		}
	}
	return nil
}

// defaultPostShutdown kills the current process the parameter timeout is true
func defaultPostShutdown(err error) {
	if isTimeoutError(err) {
		rp.Lookup("goroutine").WriteTo(os.Stderr, 1)
		_ = cue.Close(5 * time.Second)
		syscall.Kill(0, syscall.SIGKILL)
	}
}

// watchShutdown logs a message every time the passed ticker ticks as long as the runnable is not shutdown
func (r *Runner) watchShutdown(ticker *time.Ticker, s *runnable) {
	start := time.Now()
	for {
		select {
		case t, ok := <-ticker.C:
			if !ok {
				return
			}
			r.log.WithFields(cue.Fields{"service": s.name, "since": t.Sub(start)}).Info("still shuting down")
		}
	}
}

// timeoutError is used to indicated that service init or  shutdown failed
// only used internally
type timeoutError struct {
	msg     string
	service string
	timeout time.Duration
}

func newTimeoutError(msg, service string, timeout time.Duration) *timeoutError {
	return &timeoutError{msg: msg, service: service, timeout: timeout}
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("%s service=%s after=%v", e.msg, e.service, e.timeout)
}

func (e *timeoutError) logTo(log cue.Logger) error {
	log.WithFields(cue.Fields{"service": e.service, "timeout": e.timeout}).Warn(e.msg)
	return e
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	_, r := err.(*timeoutError)
	if r {
		return true
	}
	_, inner := errors.Cause(err).(*timeoutError)
	return inner
}

func joinedServiceNames(services []*runnable) string {
	if len(services) == 0 {
		return ""
	}
	var names []string
	for _, s := range services {
		names = append(names, s.name)
	}
	return strings.Join(names, ",")
}

func reverseServices(s []*runnable) []*runnable {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
