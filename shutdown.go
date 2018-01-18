package service

import (
	"fmt"
	"os"
	"os/signal"
	rp "runtime/pprof"
	"syscall"
	"time"

	"github.com/cenkalti/backoff"
)

type ShutdownHandler func(os.Signal)

// WaitForShutdown registers signal handlers for SIGHUP, SIGINT, SIGQUIT and
// SIGTERM and shuts down the service on notification.
func WaitForShutdown(timeout time.Duration, handler ShutdownHandler) {
	ch := make(chan os.Signal, 2)

	signal.Notify(
		ch,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	sig := <-ch

	ebo := backoff.NewExponentialBackOff()
	ebo.InitialInterval = 5 * time.Second
	ebo.MaxElapsedTime = timeout
	ebo.Reset()

	go func() {
		for {
			shutdownCheck(ebo)
		}
	}()

	if handler != nil {
		handler(sig)
	}
}

func shutdownCheck(ebo *backoff.ExponentialBackOff) {
	if t := ebo.NextBackOff(); t == backoff.Stop {
		// still alive after shutdown timeout
		// let's kill ourselves
		// nolint: gas
		fmt.Fprintln(os.Stderr, "still not dead. killing myself.")
		// nolint: gas, errcheck
		syscall.Kill(0, syscall.SIGKILL)
	} else {
		time.Sleep(t)
		// nolint: gas
		fmt.Fprintln(os.Stderr, "shutdown blocked. dumping blocking goroutines:")
		// nolint: gas, errcheck
		rp.Lookup("goroutine").WriteTo(os.Stderr, 1)
	}
}
