package service

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/remerge/cue"
)

// CheckResult represents result of one registered healthcheck
type CheckResult struct {
	Age   time.Duration `json:",omitempty"` // Age of passed check. Zero if failed.
	Error string        `json:",omitempty"` // Last error
}

// ChecksHandler wraps HandleChecks method which receives
// results of all evaluated health checks.
type ChecksHandler interface {
	HandleChecks(time.Time, map[string]CheckResult)
}

// CueChecksHandler logs changed checks
type CueChecksHandler struct {
	state  map[string]string
	logger cue.Logger
}

func NewCueChecksHandler(logger cue.Logger, version string) ChecksHandler {
	return &CueChecksHandler{
		state:  map[string]string{},
		logger: logger.WithFields(cue.Fields{
			"version": version,
			"reason": "health",
		}),
	}
}

func (h *CueChecksHandler) HandleChecks(_ time.Time, checks map[string]CheckResult) {
	for name, res := range checks {
		last, ok := h.state[name]
		if !ok {
			last = "uninitialized"
		}
		if last != "" && res.Error == "" {
			h.logger.WithFields(cue.Fields{
				"code": name,
				"last_error": last,
			}).Info("pass")
		}
		if (last == "" || !ok) && res.Error != "" {
			h.logger.WithFields(cue.Fields{
				"code": name,
				"error": res.Error,
			}).Warn("fail")
		}
		h.state[name] = res.Error
	}
}

// StateChecksHandler keeps all checks in internal state
type StateChecksHandler struct {
	version string
	cache   map[string]interface{}
}

func NewStateChecksHandler(version string) *StateChecksHandler {
	return &StateChecksHandler{
		version: version,
		cache: map[string]interface{}{
			"at":      time.Now(),
			"version": version,
		},
	}
}

func (c *StateChecksHandler) HandleChecks(at time.Time, checks map[string]CheckResult) {
	c.cache = map[string]interface{}{
		"at":      at,
		"version": c.version,
		"checks":  checks,
	}
}

// State returns checks state
func (c *StateChecksHandler) State() map[string]interface{} {
	return c.cache
}

// GuardChecksHandler caches result of all required checks
type GuardChecksHandler struct {
	required []string
	failed   uint32
}

func NewGuardChecksHandler(required ...string) *GuardChecksHandler {
	return &GuardChecksHandler{
		required: required,
		failed:   1,
	}
}

func (c *GuardChecksHandler) HandleChecks(_ time.Time, checks map[string]CheckResult) {
	var v uint32
	for _, name := range c.required {
		res, ok := checks[name]
		if !ok || res.Error != "" {
			v = 1
			break
		}
	}
	atomic.StoreUint32(&c.failed, v)
}

// IsHealthy returns true if all required checks are passed
func (c *GuardChecksHandler) IsHealthy() bool {
	return atomic.LoadUint32(&c.failed) == 0
}

// HealthChecker wraps CheckHealth method to evaluate health check
type HealthChecker interface {
	CheckHealth() error
}

// NilHealthChecker always return nil as CheckHealth() result
type NilHealthChecker struct{}

func (*NilHealthChecker) CheckHealth() error {
	return nil
}

// Healthcheck holds and evaluates registered healthchecks
type Healthcheck struct {
	version         string
	metricsRegistry metrics.Registry
	interval        time.Duration
	checksHandlers  []ChecksHandler

	pool sync.Map

	running int32
	closing int32
	closeCh chan struct{}
}

// NewHealthcheck creates new Healthcheck
func NewHealthcheck(version string, pollInterval time.Duration, registry metrics.Registry, checksHandlers ...ChecksHandler) (p *Healthcheck) {
	p = &Healthcheck{
		version:         version,
		metricsRegistry: registry,
		interval:        pollInterval,
		closeCh:         make(chan struct{}),
		checksHandlers:  checksHandlers,
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

func (p *Healthcheck) loop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.closeCh:
			return
		case now := <-ticker.C:
			checks := map[string]CheckResult{}
			p.pool.Range(func(key, value interface{}) bool {
				e, ok := value.(*healthcheckEvaluator)
				if !ok {
					return true
				}
				result := e.evaluate(now)
				checks[key.(string)] = result
				return true
			})
			for _, rh := range p.checksHandlers {
				rh.HandleChecks(now, checks)
			}
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

func (e *healthcheckEvaluator) evaluate(now time.Time) (s CheckResult) {
	if err := e.handler.CheckHealth(); err != nil {
		if !e.failed {
			e.since = now
			e.failed = true
		}
		e.healthyDurationGauge.Update(0)
		return CheckResult{
			Error: fmt.Sprint(err),
		}
	}
	if e.failed {
		e.since = now
		e.failed = false
	}

	age := now.Sub(e.since)
	e.healthyDurationGauge.Update(int64(age))
	return CheckResult{
		Age: age,
	}
}
