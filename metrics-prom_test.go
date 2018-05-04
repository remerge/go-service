package service_test

import (
	"testing"

	"github.com/rcrowley/go-metrics"
	"github.com/remerge/go-service"
	"github.com/stretchr/testify/assert"
)

func TestPrometheusMetrics_Update(t *testing.T) {
	t.Run(`empty`, func(t *testing.T) {
		r := metrics.NewRegistry()
		p := service.NewPrometheusMetrics(r, "test")
		assert.NoError(t, p.Update())

		assert.Equal(t, ``, p.String())
	})
	t.Run(`base`, func(t *testing.T) {
		r := metrics.NewRegistry()
		metrics.GetOrRegisterCounter("app,l1=2 c1", r)
		metrics.GetOrRegisterCounter("app c2", r).Inc(3)
		metrics.GetOrRegisterGaugeFloat64("app,l1=2 g1", r)
		metrics.GetOrRegisterGauge("app,l1=1 g1", r)
		metrics.GetOrRegisterHistogram("app,l1=1 h1", r, metrics.NewUniformSample(104))
		metrics.GetOrRegisterMeter("app,l1=1 m1", r)
		metrics.GetOrRegisterTimer("app t1", r)
		metrics.GetOrRegisterCounter("app,label1=1,label2=2 c1", r).Inc(2)

		p := service.NewPrometheusMetrics(r, "test")
		assert.NoError(t, p.Update())

		assert.Equal(t, `
# TYPE app_c1_total counter
app_c1_total{service="test",l1="2"} 0
app_c1_total{service="test",label1="1",label2="2"} 2

# TYPE app_c2_total counter
app_c2_total{service="test"} 3

# TYPE app_g1 gauge
app_g1{service="test",l1="1"} 0
app_g1{service="test",l1="2"} 0

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

# TYPE app_h1_stddev gauge
app_h1_stddev{service="test",l1="1"} 0

# TYPE app_m1_total counter
app_m1_total{service="test",l1="1"} 0

# TYPE app_t1 summary
app_t1_count{service="test"} 0
app_t1_sum{service="test"} 0
app_t1{service="test",quantile="0.5"} 0
app_t1{service="test",quantile="0.75"} 0
app_t1{service="test",quantile="0.95"} 0
app_t1{service="test",quantile="0.99"} 0
app_t1{service="test",quantile="0.999"} 0

# TYPE app_t1_max gauge
app_t1_max{service="test"} 0

# TYPE app_t1_mean gauge
app_t1_mean{service="test"} 0

# TYPE app_t1_min gauge
app_t1_min{service="test"} 0

# TYPE app_t1_stddev gauge
app_t1_stddev{service="test"} 0
`, p.String())
	})
	t.Run("counter", func(t *testing.T) {
		r := metrics.NewRegistry()
		metrics.GetOrRegisterCounter("app no_label", r).Inc(2)
		metrics.GetOrRegisterCounter("app,l1=1 with_label", r).Inc(4)
		metrics.GetOrRegisterCounter("app,l1=2 with_label", r).Inc(5)

		p := service.NewPrometheusMetrics(r, "test")
		assert.NoError(t, p.Update())

		assert.Equal(t, `
# TYPE app_no_label_total counter
app_no_label_total{service="test"} 2

# TYPE app_with_label_total counter
app_with_label_total{service="test",l1="1"} 4
app_with_label_total{service="test",l1="2"} 5
`, p.String())
	})
	for _, td := range [][4]string{
		{
			`bad label`,
			"app,bad a",
			"[bad label \"bad\" in metric \"app,bad a\"]",
			"# ERROR bad label \"bad\" in metric \"app,bad a\"\n",
		},
		{
			"bad prefix",
			"app.1,label=1 a",
			"[bad metric name \"app.1_a\" in metric \"app.1,label=1 a\"]",
			"# ERROR bad metric name \"app.1_a\" in metric \"app.1,label=1 a\"\n",
		},
		{
			"bad name",
			"app_1,label=1 a.3",
			"[bad metric name \"app_1_a.3\" in metric \"app_1,label=1 a.3\"]",
			"# ERROR bad metric name \"app_1_a.3\" in metric \"app_1,label=1 a.3\"\n",
		},
		{
			"bad label name",
			"app_1,label.1=1 a_3",
			"[bad label name \"label.1=1\" in metric \"app_1,label.1=1 a_3\"]",
			"# ERROR bad label name \"label.1=1\" in metric \"app_1,label.1=1 a_3\"\n",
		},
	} {
		t.Run(td[0], func(t *testing.T) {
			r := metrics.NewRegistry()
			p := service.NewPrometheusMetrics(r, "test")
			metrics.GetOrRegisterCounter(td[1], r)
			assert.EqualError(t, p.Update(), td[2])
			assert.Equal(t, td[3], p.String())
		})

	}
}
