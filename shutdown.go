package service

import (
	"os"
	"os/signal"
	rp "runtime/pprof"
	"syscall"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/remerge/cue"
)

type shutdownFunc func(sig os.Signal)

var signalChannel chan os.Signal

// WaitForShutdown registers signal handlers for SIGHUP, SIGINT, SIGQUIT and
// SIGTERM and shuts down the service on notification.
func waitForShutdown(log cue.Logger, handler shutdownFunc, done chan struct{}) {
	timeout := time.Minute

	signalChannel = make(chan os.Signal, 2)

	signal.Notify(
		signalChannel,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	var signal os.Signal
	select {
	case sig := <-signalChannel:
		signal = sig
	case <-done:
		signal = syscall.SIGQUIT
	}

	ebo := backoff.NewExponentialBackOff()
	ebo.InitialInterval = 5 * time.Second
	ebo.MaxElapsedTime = timeout
	ebo.Reset()

	go func() {
		for {
			shutdownCheck(log, ebo)
		}
	}()

	if handler != nil {
		handler(signal)
	}
}

func shutdownCheck(log cue.Logger, ebo *backoff.ExponentialBackOff) {
	if t := ebo.NextBackOff(); t == backoff.Stop {
		// still alive after shutdown timeout - let's kill ourselves
		// nolint: gas
		_ = log.Error(nil, "still not dead. killing myself and dumping goroutines.")
		// nolint: gas
		_ = rp.Lookup("goroutine").WriteTo(os.Stderr, 1)
		// nolint: gas
		_ = syscall.Kill(0, syscall.SIGKILL)
	} else {
		time.Sleep(t)
		log.Warn("shutdown still blocked, waiting")
	}
}
