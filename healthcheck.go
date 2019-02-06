package service

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
)

// HealthcheckState represents overall healthcheck state
type HealthcheckState struct {
	Version string
	At      time.Time
	Healthy bool
	Checks  map[string]HealthcheckResult `json:",omitempty"`
}

// HealthcheckResult represents result of one registered healthcheck
type HealthcheckResult struct {
	Age   time.Duration `json:",omitempty"` // Age of passed check. Zero if failed.
	Error string        `json:",omitempty"` // Last error
}

// HealthChecker wraps CheckHealth method to evaluate health check
type HealthChecker interface {
	CheckHealth() error
}

// NilHealthChecker always return nil as CheckHealth() result
type NilHealthChecker struct {}

func (*NilHealthChecker) CheckHealth() error {
	return nil
}

// Healthcheck holds and evaluates registered healthchecks
type Healthcheck struct {
	version         string
	metricsRegistry metrics.Registry
	interval        time.Duration
	state           *HealthcheckState

	failedChecks int32
	at           int64
	pool         sync.Map

	running int32
	closing int32
	closeCh chan struct{}
}

// NewHealthcheck creates new Healthcheck
func NewHealthcheck(version string, pollInterval time.Duration, registry metrics.Registry) (p *Healthcheck) {
	p = &Healthcheck{
		version:         version,
		metricsRegistry: registry,
		interval:        pollInterval,
		closeCh:         make(chan struct{}),
		state: &HealthcheckState{
			Version: version,
			At:      time.Now(),
		},
	}
	return p
}

// Run starts healthcheck loop.
// This method can be safely called multiple times.
func (p *Healthcheck) Run() {
	if atomic.LoadInt32(&p.closing) == 1 || atomic.LoadInt32(&p.running) == 1 {
		return
	}
	if atomic.CompareAndSwapInt32(&p.running, 0, 1) {
		p.Register("uptime", &NilHealthChecker{})
		go p.loop()
	}
}

// Close stops healthcheck loop and prevents any further registrations.
// This method can be safely called multiple times.
func (p *Healthcheck) Close() error {
	if atomic.CompareAndSwapInt32(&p.closing, 0, 1) {
		close(p.closeCh)
	}
	<-p.closeCh
	return nil
}

// Register registers new healthcheck with given name if it not registered yet
func (p *Healthcheck) Register(name string, handler HealthChecker) {
	if atomic.LoadInt32(&p.closing) == 1 {
		return
	}
	p.pool.LoadOrStore(name, newHealthcheckEvaluator(p.metricsRegistry, name, p.version, handler))
}

// IsHealthy returns true if all registered checks are passed.
// Use to assert overall state inside service.
func (p *Healthcheck) IsHealthy() bool {
	return atomic.LoadInt32(&p.failedChecks) == 0
}

// State returns current state
func (p *Healthcheck) State() (s HealthcheckState) {
	return *p.state
}

func (p *Healthcheck) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.closeCh:
			return
		case now := <-ticker.C:
			var failed int32
			newState := &HealthcheckState{
				Version: p.version,
				At:      now,
				Checks:  map[string]HealthcheckResult{},
			}
			p.pool.Range(func(key, value interface{}) bool {
				e, ok := value.(*healthcheckEvaluator)
				if !ok {
					return true
				}
				result := e.evaluate(now)
				if result.Error != "" {
					failed++
				}
				newState.Checks[key.(string)] = result
				return true
			})
			newState.Healthy = failed == 0
			p.state = newState
			atomic.StoreInt32(&p.failedChecks, failed)
			atomic.StoreInt64(&p.at, now.UnixNano())
		}
	}
}

type healthcheckEvaluator struct {
	handler              HealthChecker
	healthyDurationGauge metrics.Gauge

	since  time.Time
	failed bool
}

func newHealthcheckEvaluator(registry metrics.Registry, name, version string, handler HealthChecker) (e *healthcheckEvaluator) {
	now := time.Now()
	e = &healthcheckEvaluator{
		handler: handler,
		healthyDurationGauge: metrics.GetOrRegisterGauge(
			fmt.Sprintf("go_service,name=%s,version=%s health", name, version),
			registry),
		since:  now,
		failed: true, // not evaluated yet
	}
	return e
}

func (e *healthcheckEvaluator) evaluate(now time.Time) (s HealthcheckResult) {
	if err := e.handler.CheckHealth(); err != nil {
		if !e.failed {
			e.since = now
			e.failed = true
		}
		e.healthyDurationGauge.Update(0)
		return HealthcheckResult{
			Error: fmt.Sprint(err),
		}
	}
	if e.failed {
		e.since = now
		e.failed = false
	}

	age := now.Sub(e.since)
	e.healthyDurationGauge.Update(int64(age))
	return HealthcheckResult{
		Age: age,
	}
}
