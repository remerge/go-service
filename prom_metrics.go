package service

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/rcrowley/go-metrics"
	lft_sample "github.com/remerge/go-lock_free_timer/sample"
)

var (
	promMetricRe      = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	promMetricLabelRe = regexp.MustCompile(`^[a-zA-Z0-9_]*$`)
	promMetricValueRe = regexp.MustCompile(`^[a-zA-Z0-9_:\-\+\.\/]*$`)
)

type metricsSampler interface {
	Sum() int64
	Count() int64
	Max() int64
	Mean() float64
	Min() int64
	StdDev() float64
	Percentiles([]float64) []float64
}

// PrometheusMetrics converts all metrics from bounded registry to
// prometheus text format and stores them in internal cache.
// See https://prometheus.io/docs/instrumenting/exposition_formats
type PrometheusMetrics struct {
	registry  metrics.Registry
	nameLabel string

	mu    sync.RWMutex
	cache bytes.Buffer
}

func NewPrometheusMetrics(registry metrics.Registry, name string) (p *PrometheusMetrics) {
	return &PrometheusMetrics{
		registry:  registry,
		nameLabel: fmt.Sprintf("service=\"%s\"", name),
	}
}

func (p *PrometheusMetrics) String() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cache.String()
}

/*
Update updates internal cache with metrics collected from bounded registry.
All entities are sorted. Update() is thread-safe.

All failures during collecting are also stored in beginning:

	# ERROR bad label "bad" in metric "app,bad a"
	# ERROR ...

Counters are represented as "XXX_counter" as well as "XXX_total" for
compatibility reasons:

	# TYPE app_c1_count counter
	app_c1_count{service="test",l1="2"} 0
	app_c1_count{service="test",label1="1",label2="2"} 2

	# TYPE app_c1_total counter
	app_c1_total{service="test",l1="2"} 0
	app_c1_total{service="test",label1="1",label2="2"} 2

Both int and float Gauges are represented as gauges:

	# TYPE app_g1 gauge
	app_g1{service="test",l1="1"} 0
	app_g1{service="test",l1="2"} 0

Timers and Histograms are represented as Prometheus summaries (see below):

	# TYPE app_h1 summary
	app_h1_count{service="test",l1="1"} 0
	app_h1_sum{service="test",l1="1"} 0
	app_h1{service="test",l1="1",quantile="0.5"} 0
	app_h1{service="test",l1="1",quantile="0.75"} 0
	app_h1{service="test",l1="1",quantile="0.95"} 0
	app_h1{service="test",l1="1",quantile="0.99"} 0
	app_h1{service="test",l1="1",quantile="0.999"} 0

	# TYPE app_h1_max gauge
	app_h1_max{service="test",l1="1"} 0

	# TYPE app_h1_mean gauge
	app_h1_mean{service="test",l1="1"} 0

	# TYPE app_h1_min gauge
	app_h1_min{service="test",l1="1"} 0

	# TYPE app_h1_p75 gauge
	app_h1_p75{service="test",l1="1"} 0

	# TYPE app_h1_p95 gauge
	app_h1_p95{service="test",l1="1"} 0

	# TYPE app_h1_p99 gauge
	app_h1_p99{service="test",l1="1"} 0

	# TYPE app_h1_p999 gauge
	app_h1_p999{service="test",l1="1"} 0

	# TYPE app_h1_stddev gauge
	app_h1_stddev{service="test",l1="1"} 0

Meters are represented as counters (see above):

	# TYPE app_m1_count counter
	app_m1_count{service="test",l1="1"} 0

	# TYPE app_m1_total counter
	app_m1_total{service="test",l1="1"} 0
*/
// nolint: gocyclo
func (p *PrometheusMetrics) Update() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// because TYPE header in prometheus should be named without labels
	// we need to restructure go-metrics registry

	var failures []string
	mTypes := map[string]string{}
	mValues := map[string][][2]string{}

	p.registry.Each(func(s string, i interface{}) {
		var name, labels string
		var err error
		if name, labels, err = p.extractSignature(s); err != nil {
			failures = append(failures, err.Error())
		}
		switch m1 := i.(type) {
		case metrics.Counter:
			p.addCounter(mTypes, mValues, name, labels, m1.Count())
		case metrics.Meter:
			p.addCounter(mTypes, mValues, name, labels, m1.Count())
		case metrics.Gauge:
			p.addGauge(mTypes, mValues, name, labels, fmt.Sprint(m1.Value()))
		case metrics.GaugeFloat64:
			p.addGauge(mTypes, mValues, name, labels, fmt.Sprint(m1.Value()))
		case metrics.Healthcheck:
			// also gauge
			val := "1"
			if m1.Error() != nil {
				val = "0"
			}
			p.addGauge(mTypes, mValues, name, labels, val)
		case metrics.Histogram:
			p.updateHistogram(mTypes, mValues, name, labels, m1)
		case metrics.Timer:
			sn := m1.Snapshot()
			if sn.Count() == 0 {
				break
			}

			p.addSummary(mTypes, mValues, name, labels, sn)
		}
	})
	return p.writeData(failures, mTypes, mValues)
}

func (p *PrometheusMetrics) updateHistogram(mTypes map[string]string, mValues map[string][][2]string, name, labels string, hst metrics.Histogram) {
	withBuckets, ok := hst.Sample().(lft_sample.SampleWithBuckets)
	if ok {
		// Amount of events is not checked here intentionally: a histogram output
		// with zero values is considered valid
		p.addBucketHistogramSummary(mTypes, mValues, name, labels, withBuckets)
	}

	sn := hst.Snapshot()
	if sn.Count() > 0 {
		p.addSummary(mTypes, mValues, name, labels, sn)
	}
}

func (p *PrometheusMetrics) writeData(failures []string, t map[string]string, v map[string][][2]string) (err error) {
	p.cache.Reset()

	// write failures
	sort.Strings(failures)
	for _, failure := range failures {
		failure = strings.Replace(failure, "\n", "", -1)

		if _, err = fmt.Fprintf(&p.cache, "# ERROR %s\n", failure); err != nil {
			return err
		}
	}

	var mNames []string
	for name := range t {
		mNames = append(mNames, name)
	}
	sort.Strings(mNames)

	for _, name := range mNames {
		if _, err = fmt.Fprintf(&p.cache, "\n# TYPE %s %s\n", name, t[name]); err != nil {
			return err
		}
		sort.Slice(v[name], func(i, j int) bool {
			return v[name][i][0] < v[name][j][0]
		})
		for _, value := range v[name] {
			if _, err = fmt.Fprintf(&p.cache, "%s %s\n", value[0], value[1]); err != nil {
				return err
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%v", failures)
	}
	return nil
}

func (p *PrometheusMetrics) addBucketHistogramSummary(t map[string]string, v map[string][][2]string, name, labels string, sampler lft_sample.SampleWithBuckets) {
	name = name + "_buckets"
	t[name] = "histogram"

	buckets, values := sampler.BucketsAndValues()
	for idx := 0; idx < len(buckets); idx++ {
		p.addV(v, name, p.fullName(name, fmt.Sprintf("%s,le=\"%f\"", labels, buckets[idx])), values[idx])
	}
	p.addV(v, name, p.fullName(name, labels+",le=\"+Inf\""), values[len(buckets)])

	p.addV(v, name, p.fullName(name+"_count", labels), sampler.Count())
	p.addV(v, name, p.fullName(name+"_sum", labels), sampler.Sum())
}

func (p *PrometheusMetrics) addSummary(t map[string]string, v map[string][][2]string, name, labels string, sampler metricsSampler) {
	t[name] = "summary"
	p.addV(v, name, p.fullName(name+"_count", labels), sampler.Count())
	p.addV(v, name, p.fullName(name+"_sum", labels), sampler.Sum())

	ps := sampler.Percentiles([]float64{0.5, 0.75, 0.95, 0.99, 0.999})
	p.addV(v, name, p.fullName(name, labels+",quantile=\"0.5\""), ps[0])
	p.addV(v, name, p.fullName(name, labels+",quantile=\"0.75\""), ps[1])
	p.addV(v, name, p.fullName(name, labels+",quantile=\"0.95\""), ps[2])
	p.addV(v, name, p.fullName(name, labels+",quantile=\"0.99\""), ps[3])
	p.addV(v, name, p.fullName(name, labels+",quantile=\"0.999\""), ps[4])

	p.addGauge(t, v, name+"_min", labels, sampler.Min())
	p.addGauge(t, v, name+"_max", labels, sampler.Max())
	p.addGauge(t, v, name+"_mean", labels, sampler.Mean())
	p.addGauge(t, v, name+"_stddev", labels, sampler.StdDev())
}

func (p *PrometheusMetrics) addCounter(t map[string]string, v map[string][][2]string, name, labels string, value int64) {
	t[name+"_total"] = "counter"
	p.addV(v, name+"_total", p.fullName(name+"_total", labels), value)
}

func (p *PrometheusMetrics) addGauge(t map[string]string, v map[string][][2]string, bind, labels string, value interface{}) {
	t[bind] = "gauge"
	p.addV(v, bind, p.fullName(bind, labels), value)
}

func (p *PrometheusMetrics) addV(v map[string][][2]string, bind, fullname string, value interface{}) {
	v[bind] = append(v[bind], [2]string{fullname, fmt.Sprint(value)})
}

func (p *PrometheusMetrics) extractSignature(raw string) (name, labels string, err error) {
	var split, lSplit []string

	if split = strings.Split(raw, " "); len(split) != 2 {
		return "", "", fmt.Errorf(`bad metric signature "%s"`, raw)
	}
	name = split[1]
	split = strings.Split(split[0], ",")
	name = prometheusMetricName(split[0] + "_" + name)
	if !promMetricRe.MatchString(name) {
		return "", "", fmt.Errorf(`bad metric name "%s" in metric "%s"`, name, raw)
	}

	var multiErr error
	for _, l := range split[1:] {
		if lSplit = strings.Split(l, "="); len(lSplit) != 2 {
			return "", "", fmt.Errorf(`bad label "%s" in metric "%s"`, l, raw)
		}

		if !promMetricLabelRe.MatchString(lSplit[0]) {
			return "", "", fmt.Errorf(`bad label name "%s" in metric "%s"`, l, raw)
		}
		if !promMetricValueRe.MatchString(lSplit[1]) {
			err = fmt.Errorf(`bad label value "%s" in metric "%s"`, l, raw)
			multiErr = multierror.Append(multiErr, err)
			continue
		}
		labels += fmt.Sprintf(`,%s="%s"`, prometheusMetricName(lSplit[0]), lSplit[1])
	}
	return name, labels, multiErr
}

func (p *PrometheusMetrics) fullName(name, labels string) (f string) {
	return fmt.Sprintf("%s{%s%s}", name, p.nameLabel, labels)
}

func prometheusMetricName(in string) (out string) {
	return strings.Replace(in, "-", "_", -1)
}
