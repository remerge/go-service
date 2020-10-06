package service_test

import (
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	lft "github.com/remerge/go-lock_free_timer"
	"github.com/remerge/go-service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusMetrics_UpdateWithHistogramAndTimerEvent(t *testing.T) {
	r := metrics.NewRegistry()
	metrics.GetOrRegisterCounter("app,l1=2 c1", r)
	metrics.GetOrRegisterCounter("app c2", r).Inc(3)
	metrics.GetOrRegisterGaugeFloat64("app,l1=2 g1", r)
	metrics.GetOrRegisterGauge("app,l1=1 g1", r)

	h1 := metrics.GetOrRegisterHistogram("app,l1=1 h1", r, metrics.NewUniformSample(104))
	h1.Update(42)
	h2 := metrics.GetOrRegisterHistogram("app,l1=1 h2", r, lft.NewLockFreeSampleWithBuckets([]float64{10, 20, 30}))
	h2.Update(5)
	h2.Update(15)
	h2.Update(25)
	h2.Update(31)

	metrics.GetOrRegisterMeter("app,l1=1 m1", r)
	timer := metrics.GetOrRegisterTimer("app t1", r)
	timer.Update(time.Second)
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
app_h1_count{service="test",l1="1"} 1
app_h1_sum{service="test",l1="1"} 42
app_h1{service="test",l1="1",quantile="0.5"} 42
app_h1{service="test",l1="1",quantile="0.75"} 42
app_h1{service="test",l1="1",quantile="0.95"} 42
app_h1{service="test",l1="1",quantile="0.99"} 42
app_h1{service="test",l1="1",quantile="0.999"} 42

# TYPE app_h1_max gauge
app_h1_max{service="test",l1="1"} 42

# TYPE app_h1_mean gauge
app_h1_mean{service="test",l1="1"} 42

# TYPE app_h1_min gauge
app_h1_min{service="test",l1="1"} 42

# TYPE app_h1_stddev gauge
app_h1_stddev{service="test",l1="1"} 0

# TYPE app_h2 summary
app_h2_count{service="test",l1="1"} 4
app_h2_sum{service="test",l1="1"} 76
app_h2{service="test",l1="1",quantile="0.5"} 20
app_h2{service="test",l1="1",quantile="0.75"} 29.5
app_h2{service="test",l1="1",quantile="0.95"} 31
app_h2{service="test",l1="1",quantile="0.99"} 31
app_h2{service="test",l1="1",quantile="0.999"} 31

# TYPE app_h2_buckets histogram
app_h2_buckets_count{service="test",l1="1"} 4
app_h2_buckets_sum{service="test",l1="1"} 76
app_h2_buckets{service="test",l1="1",le="+Inf"} 3
app_h2_buckets{service="test",l1="1",le="10.000000"} 1
app_h2_buckets{service="test",l1="1",le="20.000000"} 2
app_h2_buckets{service="test",l1="1",le="30.000000"} 3

# TYPE app_h2_max gauge
app_h2_max{service="test",l1="1"} 31

# TYPE app_h2_mean gauge
app_h2_mean{service="test",l1="1"} 19

# TYPE app_h2_min gauge
app_h2_min{service="test",l1="1"} 5

# TYPE app_h2_stddev gauge
app_h2_stddev{service="test",l1="1"} 9.899494936611665

# TYPE app_m1_total counter
app_m1_total{service="test",l1="1"} 0

# TYPE app_t1 summary
app_t1_count{service="test"} 1
app_t1_sum{service="test"} 1000000000
app_t1{service="test",quantile="0.5"} 1e+09
app_t1{service="test",quantile="0.75"} 1e+09
app_t1{service="test",quantile="0.95"} 1e+09
app_t1{service="test",quantile="0.99"} 1e+09
app_t1{service="test",quantile="0.999"} 1e+09

# TYPE app_t1_max gauge
app_t1_max{service="test"} 1000000000

# TYPE app_t1_mean gauge
app_t1_mean{service="test"} 1e+09

# TYPE app_t1_min gauge
app_t1_min{service="test"} 1000000000

# TYPE app_t1_stddev gauge
app_t1_stddev{service="test"} 0
`, p.String())
}

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

# TYPE app_m1_total counter
app_m1_total{service="test",l1="1"} 0
`, p.String())
	})
	t.Run("counter", func(t *testing.T) {
		r := metrics.NewRegistry()
		metrics.GetOrRegisterCounter("app no_label", r).Inc(2)
		metrics.GetOrRegisterCounter("app,l1=1 with-label", r).Inc(4)
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

		t.Run("if a label is invalid it still outputs valid labels and an error for the invalid ones", func(t *testing.T) {
			r := metrics.NewRegistry()
			metrics.GetOrRegisterCounter(`act,handler=click,partner=good counter`, r).Inc(2)
			metrics.GetOrRegisterCounter(`act,handler=click,partner="Shopee" counter`, r).Inc(2)
			metrics.GetOrRegisterCounter(`act,handler=click,partner=adx1005https://adclick.g.doubleclick.net/aclk?sa=L,time_since_bid=1h request`, r).Inc(3)

			p := service.NewPrometheusMetrics(r, "test")
			assert.Error(t, p.Update())

			ret := p.String()
			require.Contains(t, ret, `# ERROR bad label "partner=adx1005https://adclick.g.doubleclick.net/aclk?sa=L"`)
			require.Contains(t, ret, `bad label value "partner="Shopee"" in metric "act,handler=click,partner="Shopee" counter`)
			require.Contains(t, ret, `act_counter_total{service="test",handler="click",partner="good"} 2`)
		})
		t.Run("But valid metrics should not produce any error", func(t *testing.T) {
			r := metrics.NewRegistry()
			metrics.GetOrRegisterCounter(`userdb,as_node=10.58.171.28:3000 as_node-added-count`, r).Inc(2)
			metrics.GetOrRegisterCounter(`dataflow,kind=win,partner=exelbid,supply=native,ad_type=native,os=android,sdk=1.4.8 price`, r).Inc(2)
			metrics.GetOrRegisterCounter(`dfc_client,file_type=dm-snapshot-dw1-info raw_download_size`, r).Inc(2)
			p := service.NewPrometheusMetrics(r, "test")
			assert.NoError(t, p.Update())
		})

		t.Run(td[0], func(t *testing.T) {
			r := metrics.NewRegistry()
			p := service.NewPrometheusMetrics(r, "test")
			metrics.GetOrRegisterCounter(td[1], r)
			assert.EqualError(t, p.Update(), td[2])
			assert.Contains(t, p.String(), td[3])
		})

	}
}
