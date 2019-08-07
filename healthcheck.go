package service

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/remerge/cue"
)

// HealthCheckable is a subject which's health can be checked
type HealthCheckable interface {
	Healthy() error
}

// CheckHealth can be used to wrap functions so they fulfill the HealthCheckable interface
type CheckHealth func() error

func (f CheckHealth) Healthy() error { return f() }

type HealthReport map[string]HealthCheckResult

// HealthCheckResult is the result of a single check
// It contains the duration since the check is in a health state. If it is not healthy Error is set
type HealthCheckResult struct {
	HealthyFor time.Duration `json:"Age,omitempty"` // was age
	Error      string        `json:",omitempty"`
}

// HealthReportListener are notified via HealthReportPublished whenever a new HealthReport is available
type HealthReportListener interface {
	HealthReportPublished(time.Time, HealthReport)
}

// HealthChecker holds and evaluates registered healthchecks
type HealthChecker struct {
	version         string
	metricsRegistry metrics.Registry
	interval        time.Duration

	listeners []HealthReportListener

	mu         sync.Mutex
	evaluators map[string]*healthcheckEvaluator

	running int32
	closing int32
	closeCh chan struct{}
}

// NewDefaultHealthCheckerService calls NewDefaultHealthChecker and registers the Healthchecker as a service with a runner
// so it is started/stopped.
func NewDefaultHealthCheckerService(r *RunnerWithRegistry, mr metrics.Registry) (*HealthChecker, error) {
	hc, err := NewDefaultHealthChecker(mr)
	if err != nil {
		return nil, err
	}
	r.Add(hc)
	return hc, nil
}

// NewDefaultHealthChecker is a registry constructor function that creates a HealthChecker with sane default if
// requested from the registry
func NewDefaultHealthChecker(mr metrics.Registry) (*HealthChecker, error) {
	return NewHealthChecker(CodeVersion, time.Second*15, mr, NewHealthReportLogger(cue.NewLogger("health"), CodeVersion)), nil
}

// NewHealthChecker creates new Healthchecker, given a code version, in what interval registered checks should be executed and a set of listeners if a new HealthReport is available
func NewHealthChecker(version string, pollInterval time.Duration, registry metrics.Registry, listeners ...HealthReportListener) (p *HealthChecker) {
	h := &HealthChecker{
		version:         version,
		metricsRegistry: registry,
		interval:        pollInterval,
		closeCh:         make(chan struct{}),
		listeners:       listeners,
		evaluators:      make(map[string]*healthcheckEvaluator),
	}
	// hack - a check called uptime that is always healthy
	h.AddCheck("uptime", CheckHealth(func() error { return nil }))
	return h
}

func (h *HealthChecker) AddListener(l HealthReportListener) {
	h.listeners = append(h.listeners, l)
}

// temp to comply with service interface
func (h *HealthChecker) Init() error {
	h.Run()
	return nil
}

// temp to comply with service interface
func (h *HealthChecker) Shutdown(os.Signal) {
	h.Close()
}

// Run starts healthcheck loop.
// This method can be safely called multiple times.
func (h *HealthChecker) Run() {
	if atomic.LoadInt32(&h.closing) == 1 || atomic.LoadInt32(&h.running) == 1 {
		return
	}
	if atomic.CompareAndSwapInt32(&h.running, 0, 1) {
		go h.loop()
	}
}

// Close stops the healthcheck loop and prevents any further registrations.
// This method can be safely called multiple times.
func (h *HealthChecker) Close() error {
	if atomic.CompareAndSwapInt32(&h.closing, 0, 1) {
		close(h.closeCh)
	}
	<-h.closeCh
	return nil
}

// AddCheck registers new check by name unless it was registered before
func (h *HealthChecker) AddCheck(name string, checkable HealthCheckable) {
	if atomic.LoadInt32(&h.closing) == 1 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.evaluators[name]; !ok {
		h.evaluators[name] = newHealthcheckEvaluator(h.metricsRegistry, name, h.version, checkable)
	}
}

// Update reevaluates all checks
func (h *HealthChecker) Update() {
	if atomic.LoadInt32(&h.closing) == 1 {
		return
	}
	h.evaluate(time.Now())
}

func (h *HealthChecker) evaluate(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	report := HealthReport{}
	for name, evaluator := range h.evaluators {
		report[name] = evaluator.evaluate(now)
	}
	for _, l := range h.listeners {
		l.HealthReportPublished(now, report)
	}
}

func (h *HealthChecker) loop() {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.closeCh:
			return
		case now := <-ticker.C:
			h.evaluate(now)
		}
	}
}

// healthcheckEvaluator check a single HealthCheckable if it is Healthy and tracks
// its status (healthy?. healthy since timestamp, duration since in healthy state as a metric gauge)
// how long
type healthcheckEvaluator struct {
	checkable HealthCheckable

	healthyDurationGauge metrics.Gauge

	healthySince time.Time
	failed       bool
}

func newHealthcheckEvaluator(registry metrics.Registry, name, version string, checkable HealthCheckable) (e *healthcheckEvaluator) {
	e = &healthcheckEvaluator{
		checkable:            checkable,
		healthyDurationGauge: metrics.GetOrRegisterGauge(fmt.Sprintf("go_service,name=%s,version=%s health", name, version), registry),
		healthySince:         time.Now(),
		failed:               true, // not evaluated yet
	}
	return e
}

func (e *healthcheckEvaluator) evaluate(now time.Time) (s HealthCheckResult) {
	if err := e.checkable.Healthy(); err != nil {
		if !e.failed {
			e.healthySince = now
			e.failed = true
		}
		e.healthyDurationGauge.Update(0)
		return HealthCheckResult{
			Error: fmt.Sprint(err),
		}
	}
	if e.failed {
		e.healthySince = now
		e.failed = false
	}
	healthyFor := now.Sub(e.healthySince)
	e.healthyDurationGauge.Update(int64(healthyFor))
	return HealthCheckResult{
		HealthyFor: healthyFor,
	}
}

// HealthReportLogger generates a log message per health check if its status (healthy/unhealthy) has changed compared to the last time
// a HealthReport has been received.
type HealthReportLogger struct {
	state  map[string]string
	logger cue.Logger
}

func NewHealthReportLogger(logger cue.Logger, version string) *HealthReportLogger {
	return &HealthReportLogger{
		state: map[string]string{},
		logger: logger.WithFields(cue.Fields{
			"version": version,
			"reason":  "health",
		}),
	}
}

func (h *HealthReportLogger) HealthReportPublished(_ time.Time, report HealthReport) {
	for name, res := range report {
		last, ok := h.state[name]
		if !ok {
			last = "uninitialized"
		}
		if last != "" && res.Error == "" {
			h.logger.WithFields(cue.Fields{
				"code":       name,
				"last_error": last,
			}).Info("pass")
		}
		if (last == "" || !ok) && res.Error != "" {
			h.logger.WithFields(cue.Fields{
				"code":  name,
				"error": res.Error,
			}).Warn("fail")
		}
		h.state[name] = res.Error
	}
}

// HealthReportCache caches the last HealthReport it received
type HealthReportCache struct {
	version string
	cache   map[string]interface{}
}

func NewHealthReportCache(version string) *HealthReportCache {
	return &HealthReportCache{
		version: version,
		cache: map[string]interface{}{
			"at":      time.Now(),
			"version": version,
		},
	}
}

func (c *HealthReportCache) HealthReportPublished(at time.Time, report HealthReport) {
	c.cache = map[string]interface{}{
		"at":      at,
		"version": c.version,
		"checks":  report, // TODO: rename
	}
}

// State returns checks state
func (c *HealthReportCache) State() map[string]interface{} {
	return c.cache
}

// HealthReportEvaluator analyses a HealthReport and compares the result of checks aginst a given set of checks that need  to pass.
// If one of the checks did not pass AllHealthy will return false.
type HealthReportEvaluator struct {
	required []string
	failed   uint32
}

func NewHealthReportEvaluator(required ...string) *HealthReportEvaluator {
	return &HealthReportEvaluator{
		required: required,
		failed:   1,
	}
}

func (h *HealthReportEvaluator) HealthReportPublished(_ time.Time, report HealthReport) {
	var v uint32
	for _, name := range h.required {
		res, ok := report[name]
		if !ok || res.Error != "" {
			v = 1
			break
		}
	}
	atomic.StoreUint32(&h.failed, v)
}

// AllHealthy returns true if all required checks are passed
func (h *HealthReportEvaluator) AllHealthy() bool {
	return atomic.LoadUint32(&h.failed) == 0
}
