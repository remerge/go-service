package service

import (
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/rcrowley/go-metrics"
	lft "github.com/remerge/go-lock_free_timer"
)

var (
	memStats       runtime.MemStats
	runtimeMetrics struct {
		MemStats struct {
			Alloc         metrics.Gauge
			BuckHashSys   metrics.Gauge
			DebugGC       metrics.Gauge
			EnableGC      metrics.Gauge
			Frees         metrics.Gauge
			HeapAlloc     metrics.Gauge
			HeapIdle      metrics.Gauge
			HeapInuse     metrics.Gauge
			HeapObjects   metrics.Gauge
			HeapReleased  metrics.Gauge
			HeapSys       metrics.Gauge
			LastGC        metrics.Gauge
			Lookups       metrics.Gauge
			Mallocs       metrics.Gauge
			MCacheInuse   metrics.Gauge
			MCacheSys     metrics.Gauge
			MSpanInuse    metrics.Gauge
			MSpanSys      metrics.Gauge
			NextGC        metrics.Gauge
			NumGC         metrics.Gauge
			GCCPUFraction metrics.GaugeFloat64
			PauseNs       metrics.Histogram
			PauseTotalNs  metrics.Gauge
			StackInuse    metrics.Gauge
			StackSys      metrics.Gauge
			Sys           metrics.Gauge
			TotalAlloc    metrics.Gauge
		}
		NumCgoCall   metrics.Gauge
		NumGoroutine metrics.Gauge
		NumThread    metrics.Gauge
		ReadMemStats metrics.Timer
	}
	frees   uint64
	lookups uint64
	mallocs uint64
	numGC   uint32

	threadCreateProfile = pprof.Lookup("threadcreate")
)

// CaptureRuntimeMemStats captures new values for the Go runtime statistics
// exported in runtime.MemStats.  This is designed to be called as a goroutine.
func captureRuntimeMemStats(d time.Duration, closeChan <-chan struct{}) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	startTime := time.Now()
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
			captureRuntimeMemStatsOnce(startTime)
		}
	}
}

// Capture new values for the Go runtime statistics exported in
// runtime.MemStats.  This is designed to be called in a background goroutine.
// Giving a registry which has not been given to registerRuntimeMemStats will
// panic.
//
// Be very careful with this because runtime.ReadMemStats calls the C functions
// runtime·semacquire(&runtime·worldsema) and runtime·stoptheworld() and that
// last one does what it says on the tin.
func captureRuntimeMemStatsOnce(time.Time) {
	t := time.Now()
	runtime.ReadMemStats(&memStats) // This takes 50-200us.
	runtimeMetrics.ReadMemStats.UpdateSince(t)

	runtimeMetrics.MemStats.Alloc.Update(int64(memStats.Alloc))
	runtimeMetrics.MemStats.BuckHashSys.Update(int64(memStats.BuckHashSys))
	if memStats.DebugGC {
		runtimeMetrics.MemStats.DebugGC.Update(1)
	} else {
		runtimeMetrics.MemStats.DebugGC.Update(0)
	}
	if memStats.EnableGC {
		runtimeMetrics.MemStats.EnableGC.Update(1)
	} else {
		runtimeMetrics.MemStats.EnableGC.Update(0)
	}

	runtimeMetrics.MemStats.Frees.Update(int64(memStats.Frees - frees))
	runtimeMetrics.MemStats.HeapAlloc.Update(int64(memStats.HeapAlloc))
	runtimeMetrics.MemStats.HeapIdle.Update(int64(memStats.HeapIdle))
	runtimeMetrics.MemStats.HeapInuse.Update(int64(memStats.HeapInuse))
	runtimeMetrics.MemStats.HeapObjects.Update(int64(memStats.HeapObjects))
	runtimeMetrics.MemStats.HeapReleased.Update(int64(memStats.HeapReleased))
	runtimeMetrics.MemStats.HeapSys.Update(int64(memStats.HeapSys))
	runtimeMetrics.MemStats.LastGC.Update(int64(memStats.LastGC))
	runtimeMetrics.MemStats.Lookups.Update(int64(memStats.Lookups - lookups))
	runtimeMetrics.MemStats.Mallocs.Update(int64(memStats.Mallocs - mallocs))
	runtimeMetrics.MemStats.MCacheInuse.Update(int64(memStats.MCacheInuse))
	runtimeMetrics.MemStats.MCacheSys.Update(int64(memStats.MCacheSys))
	runtimeMetrics.MemStats.MSpanInuse.Update(int64(memStats.MSpanInuse))
	runtimeMetrics.MemStats.MSpanSys.Update(int64(memStats.MSpanSys))
	runtimeMetrics.MemStats.NextGC.Update(int64(memStats.NextGC))
	runtimeMetrics.MemStats.NumGC.Update(int64(memStats.NumGC))
	runtimeMetrics.MemStats.GCCPUFraction.Update(memStats.GCCPUFraction)

	// <https://code.google.com/p/go/source/browse/src/pkg/runtime/mgc0.c>
	i := numGC % uint32(len(memStats.PauseNs))
	ii := memStats.NumGC % uint32(len(memStats.PauseNs))
	if memStats.NumGC-numGC >= uint32(len(memStats.PauseNs)) {
		for i = 0; i < uint32(len(memStats.PauseNs)); i++ {
			runtimeMetrics.MemStats.PauseNs.Update(int64(memStats.PauseNs[i]))
		}
	} else {
		if i > ii {
			for ; i < uint32(len(memStats.PauseNs)); i++ {
				runtimeMetrics.MemStats.PauseNs.Update(int64(memStats.PauseNs[i]))
			}
			i = 0
		}
		for ; i < ii; i++ {
			runtimeMetrics.MemStats.PauseNs.Update(int64(memStats.PauseNs[i]))
		}
	}
	frees = memStats.Frees
	lookups = memStats.Lookups
	mallocs = memStats.Mallocs
	numGC = memStats.NumGC

	runtimeMetrics.MemStats.PauseTotalNs.Update(int64(memStats.PauseTotalNs))
	runtimeMetrics.MemStats.StackInuse.Update(int64(memStats.StackInuse))
	runtimeMetrics.MemStats.StackSys.Update(int64(memStats.StackSys))
	runtimeMetrics.MemStats.Sys.Update(int64(memStats.Sys))
	runtimeMetrics.MemStats.TotalAlloc.Update(int64(memStats.TotalAlloc))

	runtimeMetrics.NumCgoCall.Update(runtime.NumCgoCall())

	runtimeMetrics.NumGoroutine.Update(int64(runtime.NumGoroutine()))

	runtimeMetrics.NumThread.Update(int64(threadCreateProfile.Count()))
}

// Register runtimeMetrics for the Go runtime statistics exported in runtime
// and specifically runtime.MemStats.  The runtimeMetrics are named by their
// fully-qualified Go symbols, i.e. runtime.MemStats.Alloc.
func registerRuntimeMemStats(r metrics.Registry) {
	runtimeMetrics.MemStats.Alloc = metrics.NewGauge()
	runtimeMetrics.MemStats.BuckHashSys = metrics.NewGauge()
	runtimeMetrics.MemStats.DebugGC = metrics.NewGauge()
	runtimeMetrics.MemStats.EnableGC = metrics.NewGauge()
	runtimeMetrics.MemStats.Frees = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapAlloc = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapIdle = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapInuse = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapObjects = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapReleased = metrics.NewGauge()
	runtimeMetrics.MemStats.HeapSys = metrics.NewGauge()
	runtimeMetrics.MemStats.LastGC = metrics.NewGauge()
	runtimeMetrics.MemStats.Lookups = metrics.NewGauge()
	runtimeMetrics.MemStats.Mallocs = metrics.NewGauge()
	runtimeMetrics.MemStats.MCacheInuse = metrics.NewGauge()
	runtimeMetrics.MemStats.MCacheSys = metrics.NewGauge()
	runtimeMetrics.MemStats.MSpanInuse = metrics.NewGauge()
	runtimeMetrics.MemStats.MSpanSys = metrics.NewGauge()
	runtimeMetrics.MemStats.NextGC = metrics.NewGauge()
	runtimeMetrics.MemStats.NumGC = metrics.NewGauge()
	runtimeMetrics.MemStats.GCCPUFraction = metrics.NewGaugeFloat64()
	runtimeMetrics.MemStats.PauseNs = metrics.NewHistogram(
		lft.NewLockFreeSample(1028))
	runtimeMetrics.MemStats.PauseTotalNs = metrics.NewGauge()
	runtimeMetrics.MemStats.StackInuse = metrics.NewGauge()
	runtimeMetrics.MemStats.StackSys = metrics.NewGauge()
	runtimeMetrics.MemStats.Sys = metrics.NewGauge()
	runtimeMetrics.MemStats.TotalAlloc = metrics.NewGauge()
	runtimeMetrics.NumCgoCall = metrics.NewGauge()
	runtimeMetrics.NumGoroutine = metrics.NewGauge()
	runtimeMetrics.NumThread = metrics.NewGauge()
	runtimeMetrics.ReadMemStats = lft.NewLockFreeTimer()

	_ = r.Register("go_runtime mem_stat_alloc",
		runtimeMetrics.MemStats.Alloc)
	_ = r.Register("go_runtime mem_stat_buck_hash_sys",
		runtimeMetrics.MemStats.BuckHashSys)
	_ = r.Register("go_runtime mem_stat_debug_gc",
		runtimeMetrics.MemStats.DebugGC)
	_ = r.Register("go_runtime mem_stat_enable_gc",
		runtimeMetrics.MemStats.EnableGC)
	_ = r.Register("go_runtime mem_stat_frees",
		runtimeMetrics.MemStats.Frees)
	_ = r.Register("go_runtime mem_stat_heap_alloc",
		runtimeMetrics.MemStats.HeapAlloc)
	_ = r.Register("go_runtime mem_stat_heap_idle",
		runtimeMetrics.MemStats.HeapIdle)
	_ = r.Register("go_runtime mem_stat_heap_inuse",
		runtimeMetrics.MemStats.HeapInuse)
	_ = r.Register("go_runtime mem_stat_heap_objects",
		runtimeMetrics.MemStats.HeapObjects)
	_ = r.Register("go_runtime mem_stat_heap_released",
		runtimeMetrics.MemStats.HeapReleased)
	_ = r.Register("go_runtime mem_stat_heap_sys",
		runtimeMetrics.MemStats.HeapSys)
	_ = r.Register("go_runtime mem_stat_last_gc",
		runtimeMetrics.MemStats.LastGC)
	_ = r.Register("go_runtime mem_stat_lookups",
		runtimeMetrics.MemStats.Lookups)
	_ = r.Register("go_runtime mem_stat_m_allocs",
		runtimeMetrics.MemStats.Mallocs)
	_ = r.Register("go_runtime mem_stat_m_cache_inuse",
		runtimeMetrics.MemStats.MCacheInuse)
	_ = r.Register("go_runtime mem_stat_m_cache_sys",
		runtimeMetrics.MemStats.MCacheSys)
	_ = r.Register("go_runtime mem_stat_m_span_inuse",
		runtimeMetrics.MemStats.MSpanInuse)
	_ = r.Register("go_runtime mem_stat_m_span_sys",
		runtimeMetrics.MemStats.MSpanSys)
	_ = r.Register("go_runtime mem_stat_next_gc",
		runtimeMetrics.MemStats.NextGC)
	_ = r.Register("go_runtime mem_stat_num_gc",
		runtimeMetrics.MemStats.NumGC)
	_ = r.Register("go_runtime mem_stat_gc_cpu_fraction",
		runtimeMetrics.MemStats.GCCPUFraction)
	_ = r.Register("go_runtime mem_stat_pause_ns",
		runtimeMetrics.MemStats.PauseNs)
	_ = r.Register("go_runtime mem_stat_pause_total_ns",
		runtimeMetrics.MemStats.PauseTotalNs)
	_ = r.Register("go_runtime mem_stat_stack_inuse",
		runtimeMetrics.MemStats.StackInuse)
	_ = r.Register("go_runtime mem_stat_stack_sys",
		runtimeMetrics.MemStats.StackSys)
	_ = r.Register("go_runtime mem_stat_sys",
		runtimeMetrics.MemStats.Sys)
	_ = r.Register("go_runtime mem_stat_total_alloc",
		runtimeMetrics.MemStats.TotalAlloc)
	_ = r.Register("go_runtime num_cgo_call",
		runtimeMetrics.NumCgoCall)
	_ = r.Register("go_runtime num_goroutine",
		runtimeMetrics.NumGoroutine)
	_ = r.Register("go_runtime num_thread",
		runtimeMetrics.NumThread)
	_ = r.Register("go_runtime read_mem_stats",
		runtimeMetrics.ReadMemStats)
}

// nolint: unparam
func (s *Executor) flushMetrics(freq time.Duration) {
	registerRuntimeMemStats(s.metricsRegistry)
	go captureRuntimeMemStats(freq, s.stopC)

	ticker := time.NewTicker(freq)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopC:
			return
		case <-ticker.C:
			if flushErr := s.promMetrics.Update(); flushErr != nil {
				s.Log.Warnf("failures while collect metrics: %v", flushErr)
			}
		}
	}
}
