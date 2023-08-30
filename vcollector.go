/*
  vCollector implements 2 distinct operations :
  1. Scrape VCenters for metrics. These metrics are stored in vCollector.Metrics array
  2. Serve these metrics when Prometheus connect to the endpoint

  Scraping VCenter starts at execution, then is launched each time Prometheus connect to the endpoint.
  This means metrics served to prometheus are not instant, they are previously acquired dataset.

  This assume VCenter scraping time to be shorter than Prometheus scraping interval, else data won't be ready for serving.
  (prometheus will have to wait for data availability)
*/

/* We distinguish 2 types of metrics :
  - standard metrics
  - performance metrics
  
  While requesting standard metrics is quick and easy on vcenter, performance metrics are heavy and can take
	minutes on large Vcenter (> 1000 VMs and/or 100 esx hosts)
  To handle this, we have 2 collecting threads
*/

package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"strings"
	"sync"
	"time"
)

const xver = "1.4.7d"

type vCollector struct {
	desc        string
	StdMetrics  []vMetric
	PerfMetrics []vMetric

	StdLock     sync.Mutex
	PerfLock    sync.Mutex

	// Am I currently collecting metrics?
	StdCollecting   bool
	PerfCollecting  bool

	ThreadGauge	*prometheus.GaugeVec
	TimeGauge	*prometheus.GaugeVec
}

func timeTrack(start time.Time, name string, timeg *prometheus.GaugeVec) {
	narr := strings.Split(name, "(")
	elapsed := time.Since(start)
	//log.Printf("%s took %s", name, elapsed)
	timeg.WithLabelValues(strings.TrimRight(narr[1], ")"), narr[0]).Set(float64(elapsed / time.Millisecond))
}


func (c *vCollector) Describe(ch chan<- *prometheus.Desc) {

}


func collectVCenterStd(vc HostConfig, threadg *prometheus.GaugeVec, timeg *prometheus.GaugeVec) []vMetric {
	var metrics []vMetric
	var mu sync.Mutex

	wg := sync.WaitGroup{}
	
	// TODO : Remove me
	log.Debugf("collectVCenterStd(%s) started", vc.Host)

	// Datastore Metrics
	wg.Add(1)
	go func() {
		threadg.WithLabelValues(vc.Host, "DSMetrics").Inc()
		defer threadg.WithLabelValues(vc.Host, "DSMetrics").Dec()
		defer wg.Done()
		defer timeTrack(time.Now(), fmt.Sprintf("DSMetrics(%s)", vc.Host), timeg)
		cm := DSMetrics(vc)
		mu.Lock()
		metrics = append(metrics, cm...)
		mu.Unlock()
	}()

	// Cluster Metrics
	if cfg.ClusterStats == true {
		wg.Add(1)
		go func() {
			threadg.WithLabelValues(vc.Host, "ClusterMetrics").Inc()
			defer threadg.WithLabelValues(vc.Host, "ClusterMetrics").Dec()
			defer wg.Done()
			defer timeTrack(time.Now(), fmt.Sprintf("ClusterMetrics(%s)", vc.Host), timeg)
			cm := ClusterMetrics(vc)
			mu.Lock()
			metrics = append(metrics, cm...)
			mu.Unlock()
		}()
	}

	// Cluster Counters
	if cfg.ClusterStats == true {
		wg.Add(1)
		go func() {
			threadg.WithLabelValues(vc.Host, "ClusterCounters").Inc()
			defer threadg.WithLabelValues(vc.Host, "ClusterCounters").Dec()
			defer wg.Done()
			defer timeTrack(time.Now(), fmt.Sprintf("ClusterCounters(%s)", vc.Host), timeg)
			cm := ClusterCounters(vc)
			mu.Lock()
			metrics = append(metrics, cm...)
			mu.Unlock()
		}()
	}

	// Host Metrics
	wg.Add(1)
	go func() {
		threadg.WithLabelValues(vc.Host, "HostMetrics").Inc()
		defer threadg.WithLabelValues(vc.Host, "HostMetrics").Dec()
		defer wg.Done()
		defer timeTrack(time.Now(), fmt.Sprintf("HostMetrics(%s)", vc.Host), timeg)
		cm := HostMetrics(vc)
		mu.Lock()
		metrics = append(metrics, cm...)
		mu.Unlock()
	}()

	// Host Counters
	wg.Add(1)
	go func() {
		threadg.WithLabelValues(vc.Host, "HostCounters").Inc()
		defer threadg.WithLabelValues(vc.Host, "HostCounters").Dec()
		defer wg.Done()
		defer timeTrack(time.Now(), fmt.Sprintf("HostCounters(%s)", vc.Host), timeg)
		cm := HostCounters(vc)
		mu.Lock()
		metrics = append(metrics, cm...)
		mu.Unlock()
	}()

	// VM Metrics
	if cfg.VmStats == true {
		wg.Add(1)
		go func() {
			threadg.WithLabelValues(vc.Host, "VMMetrics").Inc()
			defer threadg.WithLabelValues(vc.Host, "VMMetrics").Dec()
			defer wg.Done()
			defer timeTrack(time.Now(), fmt.Sprintf("VMMetrics(%s)", vc.Host), timeg)
			cm := VmMetrics(vc)
			mu.Lock()
			metrics = append(metrics, cm...)
			mu.Unlock()
		}()
	}

	// HBA Status
	wg.Add(1)
	go func() {
		threadg.WithLabelValues(vc.Host, "HostHBAStatus").Inc()
		defer threadg.WithLabelValues(vc.Host, "HostHBAStatus").Dec()
		defer wg.Done()
		defer timeTrack(time.Now(), fmt.Sprintf("HostHBAStatus(%s)", vc.Host), timeg)
		cm := HostHBAStatus(vc)
		mu.Lock()
		metrics = append(metrics, cm...)
		mu.Unlock()
	}()

	wg.Wait()
	log.Debugf("collectVCenterStd(%s) finished", vc.Host)

	return metrics
}

// Obsolete, directly call metrics = PerfCounters(vc)
// Perf counter collecting is heavy on vcenter with lots of VM/hosts (> 1000/100)
func collectVCenterPerf(vc *HostConfig, threadg *prometheus.GaugeVec, timeg *prometheus.GaugeVec) []vMetric {
	var metrics []vMetric

	log.Debugf("collectVCenterPerf(%s) started", vc.Host)
	threadg.WithLabelValues(vc.Host, "collectVCenterPerf").Inc()
	defer threadg.WithLabelValues(vc.Host, "collectVCenterPerf").Dec()
	defer timeTrack(time.Now(), fmt.Sprintf("collectVCenterPerf(%s)", vc.Host), timeg)
	metrics = PerfCounters(vc)
	log.Debugf("collectVCenterPerf(%s) finished", vc.Host)

	return metrics
}

func (c *vCollector) CollectStdMetricsFromVmware() {
	wg := sync.WaitGroup{}

	// Cant be set inside the goroutines loop b/c it would lead to race condition (wg.Wait will match 0 before the Nth run would wg.Add(1))
	wg.Add(len(cfg.Hosts))

	// TODO : How to clear only "just finished collecting vcenter" metrics?
	c.StdMetrics = c.StdMetrics[:0]
	
	for _, vc := range cfg.Hosts {
		go func(cvc HostConfig) {
			defer wg.Done()
			sm := collectVCenterStd(cvc, c.ThreadGauge, c.TimeGauge)
			c.StdLock.Lock()
			c.StdMetrics = append(c.StdMetrics, sm...)
			c.StdLock.Unlock()
		}(vc)
	}
	wg.Wait()
	c.StdCollecting = false
}

func (c *vCollector) CollectPerfMetricsFromVmware() {
	wg := sync.WaitGroup{}

	// Cant be set inside the goroutines loop b/c it would lead to race condition (wg.Wait will match 0 before the Nth run would wg.Add(1))
	wg.Add(len(cfg.Hosts))

	// TODO : How to clear only "just finished collecting vcenter" metrics?
	c.PerfMetrics = c.PerfMetrics[:0]
	for i, _ := range cfg.Hosts {
		go func(cvc *HostConfig) {
			defer wg.Done()
			pm := collectVCenterPerf(cvc, c.ThreadGauge, c.TimeGauge)
			c.PerfLock.Lock()
			c.PerfMetrics = append(c.PerfMetrics, pm...)
			c.PerfLock.Unlock()
		}(&cfg.Hosts[i])
	}
	wg.Wait()
	c.PerfCollecting = false
}

// Automatically called to get the metric values when Prometheus connect to endpoint
// This function serve the metrics previously collected, then notify collector to start scraping another set of metrics
func (c *vCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("vmware_exporter_version", "go-vmware-export Version", []string{}, prometheus.Labels{"version": xver}),
		prometheus.GaugeValue,
		1,
	)

	// Make sure we can access data without being overwrote by a collecting thread
	c.StdLock.Lock()
	log.Debug("Collect(): StdLock acquired")
	for _, m := range c.StdMetrics {
		switch m.mtype {
		case Gauge:
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
				prometheus.GaugeValue,
				float64(m.value),
			)
		case Counter:
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
				prometheus.CounterValue,
				float64(m.value),
			)
		default:
			log.Error("Metric type not implemented: %v", m.mtype)
		}
	}
	c.StdLock.Unlock()
	log.Debug("Collect(): StdLock released")
	if false == c.StdCollecting {
		c.StdCollecting = true
		go c.CollectStdMetricsFromVmware()
	}
	
	if (len(cfg.VmPerfCounters) > 0) || (len(cfg.HostPerfCounters) > 0 ) {
		c.PerfLock.Lock()
		log.Debug("Collect(): PerfLock acquired")
		for _, m := range c.PerfMetrics {
			switch m.mtype {
			case Gauge:
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
					prometheus.GaugeValue,
					float64(m.value),
				)
			case Counter:
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
					prometheus.CounterValue,
					float64(m.value),
				)
			default:
				log.Error("Metric type not implemented: %v", m.mtype)
			}
		}
		c.PerfLock.Unlock()
		log.Debug("Collect(): PerfLock released")
		if false == c.PerfCollecting {
			c.PerfCollecting = true
			go c.CollectPerfMetricsFromVmware()
		}
	}
}

func NewvCollector() *vCollector {
	threadg := promauto.NewGaugeVec(prometheus.GaugeOpts{
                Name: "vsphere_collector_threads_cnt",
                Help: "Number of currently running collector threads",
        }, []string{"vcenter", "function"})
	timeg := promauto.NewGaugeVec(prometheus.GaugeOpts{
                Name: "vsphere_collector_threads_time_ms",
                Help: "Completion time for collector threads, in milliseconds",
        }, []string{"vcenter", "function"})

	return &vCollector{
		desc: "vmware Exporter",
		ThreadGauge: threadg,
		TimeGauge: timeg,
	}
}

func GetClusterMetricMock() []vMetric {
	ms := []vMetric{
		{name: "cluster_mem_ballooned", help: "Usage for a", value: 10, labels: map[string]string{"vcenter": "vcenter1.vmware.lan", "cluster": "apa", "host": "04"}},
		{name: "cluster_mem_compressed", help: "Usage for a", value: 10, labels: map[string]string{"vcenter": "vcenter1.vmware.lan", "cluster": "apa", "host": "04"}},
		{name: "cluster_mem_consumedOverhead", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_distributedMemoryEntitlement", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_guest", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_usage", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_overhead", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_private", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_staticMemoryEntitlement", help: "Usage for a", value: 10, labels: map[string]string{}},
		{name: "cluster_mem_limit", help: "Usage for a", value: 10, labels: map[string]string{}},
	}
	return ms
}
