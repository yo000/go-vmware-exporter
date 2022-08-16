package main

import (
  "fmt"
	log "github.com/sirupsen/logrus"
	"github.com/prometheus/client_golang/prometheus"
	"sync"
	"time"
)

const xver = "1.1"


type vCollector struct {
	desc string
}

func timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	log.Printf("%s took %s", name, elapsed)
}

func (c *vCollector) Describe(ch chan<- *prometheus.Desc) {

	metrics := make(chan prometheus.Metric)
	go func() {
		c.Collect(metrics)
		close(metrics)
	}()
	for m := range metrics {
		ch <- m.Desc()
	}
}

func collectVCenter(vc HostConfig, ch chan<- prometheus.Metric) {
  wg := sync.WaitGroup{}
  // Datastore Metrics
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("DSMetrics(%s)", vc.Host))
    cm := DSMetrics(vc)
    for _, m := range cm {

      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
        prometheus.GaugeValue,
        float64(m.value),
      )
    }

  }()

  // Cluster Metrics
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("ClusterMetrics(%s)", vc.Host))
    cm := ClusterMetrics(vc)
    for _, m := range cm {
      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
        prometheus.GaugeValue,
        float64(m.value),
      )
    }
  }()

  // Cluster Counters
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("ClusterCounters(%s)", vc.Host))
    cm := ClusterCounters(vc)
    for _, m := range cm {
      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
        prometheus.CounterValue,
        float64(m.value),
      )
    }
  }()

  // Host Metrics
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostMetrics(%s)", vc.Host))
    cm := HostMetrics(vc)
    for _, m := range cm {
      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{"vcenter", "cluster", "host"}, nil),
        prometheus.GaugeValue,
        float64(m.value),
        m.labels["vcenter"],
        m.labels["cluster"],
        m.labels["host"],
      )
    }
  }()

  // Host Counters
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostCounters(%s)", vc.Host))
    cm := HostCounters(vc)
    for _, m := range cm {
      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{"vcenter", "cluster", "host"}, nil),
        prometheus.GaugeValue,
        float64(m.value),
        m.labels["vcenter"],
        m.labels["cluster"],
        m.labels["host"],
      )
    }
  }()

  // VM Metrics
  if cfg.vmStats == true {
    wg.Add(1)
    go func() {
      defer wg.Done()
      defer timeTrack(time.Now(), fmt.Sprintf("VMMetrics(%s)", vc.Host))
      cm := VmMetrics(vc)
      for _, m := range cm {
        ch <- prometheus.MustNewConstMetric(
          prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
          prometheus.GaugeValue,
          float64(m.value),
        )
      }

    }()
  }

  // HBA Status
  wg.Add(1)
  go func() {
    defer wg.Done()
    defer timeTrack(time.Now(), fmt.Sprintf("HostHBAStatus(%s)", vc.Host))
    cm := HostHBAStatus(vc)
    for _, m := range cm {
      ch <- prometheus.MustNewConstMetric(
        prometheus.NewDesc(m.name, m.help, []string{}, m.labels),
        prometheus.GaugeValue,
        float64(m.value),
      )
    }

  }()
  
  wg.Wait()
}

func (c *vCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("vmware_exporter_version", "go-vmware-export Version", []string{}, prometheus.Labels{"version": xver}),
		prometheus.GaugeValue,
		1,
	)

  for _, vc := range cfg.Hosts {
    collectVCenter(vc, ch)
  }
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
