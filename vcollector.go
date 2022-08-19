/*
  vCollector implements 2 distinct operations :
  1. Scrape VCenters for metrics. These metrics are stored in vCollector.Metrics array
  2. Serve these metrics when Prometheus connect to the endpoint
  
  Scraping VCenter starts at execution, then is launched each time Prometheus connect to the endpoint.
  This means metrics served to prometheus are not instant, they are previously acquired dataset.
  
  This assume VCenter scraping time to be shorter than Prometheus scraping interval, else data won't be ready for serving.
  (prometheus will have to wait for data availability)
*/

package main

import (
  "fmt"
	log "github.com/sirupsen/logrus"
	"github.com/prometheus/client_golang/prometheus"
	"sync"
	"time"
)

const xver = "1.3"


type vCollector struct {
  desc        string
  Metrics     []vMetric
  // This is to tell Collect() that data are ready to be served
  IsCollected bool
  // This mutex prevent collectVCenter() race conditions when parallel processing vcenters
  sync.Mutex
}

func timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	log.Printf("%s took %s", name, elapsed)
}

func (c *vCollector) Describe(ch chan<- *prometheus.Desc) {
  // FIXME: We use Describe as an init
  metrics := make(chan prometheus.Metric)
	go func() {
    c.CollectMetricsFromVmware()
    c.Collect(metrics)
    close(metrics)
  }()
  
	for m := range metrics {
		ch <- m.Desc()
	}
}

func collectVCenter(vc HostConfig) []vMetric {
  var metrics []vMetric
  var mu      sync.Mutex
  
  wg := sync.WaitGroup{}
  
  // Datastore Metrics
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("DSMetrics(%s)", vc.Host))
    cm := DSMetrics(vc)
    mu.Lock()
    metrics = append(metrics, cm...)
    mu.Unlock()
  }()

  // Cluster Metrics
  if cfg.clusterStats == true {
    wg.Add(1)
    go func() {
      defer wg.Done()
      defer timeTrack(time.Now(), fmt.Sprintf("ClusterMetrics(%s)", vc.Host))
      cm := ClusterMetrics(vc)
      mu.Lock()
      metrics = append(metrics, cm...)
      mu.Unlock()
    }()
  }

  // Cluster Counters
  if cfg.clusterStats == true {
    wg.Add(1)
    go func() {
      defer wg.Done()
      defer timeTrack(time.Now(), fmt.Sprintf("ClusterCounters(%s)", vc.Host))
      cm := ClusterCounters(vc)
      mu.Lock()
      metrics = append(metrics, cm...)
      mu.Unlock()
    }()
  }

  // Host Metrics
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostMetrics(%s)", vc.Host))
    cm := HostMetrics(vc)
    mu.Lock()
    metrics = append(metrics, cm...)
    mu.Unlock()
  }()

  // Host Counters
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostCounters(%s)", vc.Host))
    cm := HostCounters(vc)
    mu.Lock()
    metrics = append(metrics, cm...)
    mu.Unlock()
  }()

  // VM Metrics
  if cfg.vmStats == true {
    wg.Add(1)
    go func() {
      defer wg.Done()
      defer timeTrack(time.Now(), fmt.Sprintf("VMMetrics(%s)", vc.Host))
      cm := VmMetrics(vc)
      mu.Lock()
      metrics = append(metrics, cm...)
      mu.Unlock()
    }()
  }

  // HBA Status
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostHBAStatus(%s)", vc.Host))
    cm := HostHBAStatus(vc)
    mu.Lock()
    metrics = append(metrics, cm...)
    mu.Unlock()
  }()
  
  wg.Wait()
  
  return metrics
}


func (c *vCollector) CollectMetricsFromVmware() {
  var mu sync.Mutex
  wg := sync.WaitGroup{}
  
  // Flush previoulsy acquired metrics, then lock so Collect can't use currently collecting data
  c.Metrics = nil
  c.IsCollected = false

  // Cant be set inside the goroutines loop b/c it would lead to race condition (wg.Wait will match 0 before the Nth run would wg.Add(1))
  wg.Add(len(cfg.Hosts))
  
  log.Debugf("Length of cfg.Hosts: %d", len(cfg.Hosts))

  for _, vc := range cfg.Hosts {
    go func(cvc HostConfig) {
      m := collectVCenter(cvc)
      mu.Lock()
      c.Metrics = append(c.Metrics, m...)
      mu.Unlock()
      wg.Done()
    }(vc)
  }
  wg.Wait()
  c.IsCollected = true
}

// Automatically called to get the metric values when Prometheus connect to endpoint
// This function serve the metrics previously collected, then notify collector to start scraping another set of metrics
func (c *vCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("vmware_exporter_version", "go-vmware-export Version", []string{}, prometheus.Labels{"version": xver}),
		prometheus.GaugeValue,
		1,
	)

  // Wait for previous collection to be done. TODO : Use a channel to be notified?
  if false == c.IsCollected {
    log.Warning("Exporter was scraped before it can acquire vmware metrics, consider increasing scrape_interval")
  }
  for false == c.IsCollected {
    time.Sleep(500 * time.Millisecond)
  }
  for _, m := range c.Metrics  {
    switch (m.mtype) {
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
  
  // Now execute scraping thread
  go c.CollectMetricsFromVmware()
}

func NewvCollector() *vCollector {
	return &vCollector{
		desc: "vmware Exporter",
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
