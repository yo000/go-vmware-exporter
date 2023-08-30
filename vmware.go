package main

import (
	"math"
	"time"
	"errors"
	"context"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/performance"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

type MetricType int

const (
	Counter MetricType = iota + 1
	Gauge
	Histogram
	Summary
)

type vMetric struct {
	name   string
	mtype  MetricType
	help   string
	value  float64
	labels map[string]string
}

// Connect to vCenter
func NewClient(vc HostConfig, ctx context.Context) (*govmomi.Client, error) {

	u, err := url.Parse("https://" + vc.Host + vim25.Path)
	if err != nil {
		log.Fatal(err)
	}
	u.User = url.UserPassword(vc.User, vc.Password)
	log.Debugf("Connecting to %s", u.String())

	return govmomi.NewClient(ctx, u, true)
}

/* v1.4.1 :  Collectors can panic if host suddenly goes offline, like in a network outage.
    We nneed to recover from this
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0xb64ac5]

goroutine 7050870 [running]:
main.HostCounters({{0xc0000cc3a8, 0x18}, {0xc0000a74d4, 0xb}, {0xc0000cc3c0, 0x14}})
        /home/yo/Dev/go/go-vmware-exporter/vmware.go:509 +0xd85
*/
func recoverCollector() {
	if r := recover(); r != nil {
		log.Errorf("recovered from %v", r)
	}
}

/* Will replace all std metrics fct
func StdMetrics(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric

	// Create Web client, with its context
	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)
}
*/


// Handle context timeout ("Post "https://hbgt-vcenter4.hbgt.intra/sdk": context deadline exceeded")
//  by cancelling previous, creating a new context.
// New connection will be handled where needed (ie when getting metrics from vcenter)
// metricType = "std" ou "perf"
// FIXME : Recuperer cette valeur "metricType" automatiquement
func handleError(err error, vc  *HostConfig, metricType string) {
	if strings.HasSuffix(err.Error(), "context deadline exceeded") {
		log.Errorf("Timeout : %s", err.Error())
		log.Errorf("Création d'un nouveau context pour %s", metricType)

		switch metricType {
			case "std":
				if(vc.StdConnection.Client != nil) {
					log.Debugf("Std Client Logout")
					vc.StdConnection.Client.Logout(vc.StdConnection.Context)
				}
				if(vc.StdConnection.CancelContext != nil) {
					log.Debugf("Cancel Std Context")
					vc.StdConnection.CancelContext()
				}
			case "perf":
				if(vc.PerfConnection.Client != nil) {
					log.Debugf("Perf Client Logout")
					vc.PerfConnection.Client.Logout(vc.PerfConnection.Context)
				}
				if(vc.PerfConnection.CancelContext != nil) {
					log.Debugf("Cancel Perf Context")
					vc.PerfConnection.CancelContext()
				}
		}
	} else {
		log.Errorf("Error in %s collector: %s", err.Error())
	}
}

func checkConnection(vc *HostConfig, metricType string) error {
	var err error

	// Create connection if needed
	switch metricType {
		case "std":
			if (vc.StdConnection.Client == nil) {
				log.Debug("Create std connection")
				vc.StdConnection.Client, err = NewClient(*vc, vc.StdConnection.Context)
				if err != nil {
					return err
				}
			}
		case "perf":
			if (vc.PerfConnection.Client == nil) {
				log.Debug("Create perf connection")
				vc.PerfConnection.Client, err = NewClient(*vc, vc.PerfConnection.Context)
				if err != nil {
					return err
				}
			}
	}
	return nil
}

// Replace HostPerfCounters and VmPerfCounters
func PerfCounters(vc *HostConfig) []vMetric {
	//defer recoverCollector()

	var metrics []vMetric
	var err error

	if err = checkConnection(vc, "perf"); err != nil {
		log.Errorf("Error connecting to %s: %s", vc.Host, err)
		return metrics
	}

	m := view.NewManager(vc.PerfConnection.Client.Client)
	defer m.Destroy(vc.PerfConnection.Context)

	var moTypes []string
	if len(cfg.VmPerfCounters) > 0 {
		moTypes = append(moTypes, "VirtualMachine")
	}
	if len(cfg.HostPerfCounters) > 0 {
		moTypes = append(moTypes, "HostSystem")
	}

	log.Debugf("Creating view for object types %v", moTypes)

	v, err := m.CreateContainerView(vc.PerfConnection.Context, vc.PerfConnection.Client.ServiceContent.RootFolder, moTypes, true)
	if err != nil {
		handleError(err, vc, "perf")
		return metrics
	}
	if v != nil {
		defer v.Destroy(vc.PerfConnection.Context)
	}

	// Create a PerfManager
	perfManager := performance.NewManager(vc.PerfConnection.Client.Client)

	// Retrieve counters name list
	cnt, err := perfManager.CounterInfoByName(vc.PerfConnection.Context)
	if err != nil {
		handleError(err, vc, "perf")
		return metrics
	}

	// Filter wanted metrics
	var names []string
	for _, cn := range cfg.HostPerfCounters {
		for name := range cnt {
			if strings.EqualFold(cn.VName, name) {
				names = append(names, name)
			}
		}
	}
	for _, cn := range cfg.VmPerfCounters {
		for name := range cnt {
			if strings.EqualFold(cn.VName, name) {
				names = append(names, name)
			}
		}
	}

	// Create PerfQuerySpec
	spec := types.PerfQuerySpec{
		MaxSample:  1,
		MetricId:   []types.PerfMetricId{{Instance: "*"}},
		IntervalId: 300,
	}

	// Get objects references
	moRefs, err := v.Find(vc.PerfConnection.Context, moTypes, nil)
	if err != nil {
		handleError(err, vc, "perf")
		return metrics
	}

	// Query metrics
	sample, err := perfManager.SampleByName(vc.PerfConnection.Context, spec, names, moRefs)
	if err != nil {
		handleError(err, vc, "perf")
		//v.Destroy(vc.PerfConnection.Context)
		return metrics
	}

	result, err := perfManager.ToMetricSeries(vc.PerfConnection.Context, sample)
	if err != nil {
		log.Error(err.Error())
		return metrics
	}

	// Read result
	for _, metric := range result {
		switch metric.Entity.Type {
			case "VirtualMachine":
				m, _ := GetVmMetricsFromEntity(vc, metric)
				metrics = append(metrics, m...)
			case "HostSystem":
				m, _ := GetHostMetricsFromEntity(vc, v, metric)
				metrics = append(metrics, m...)
			default:
				log.Infof("Unknown entity type (%s) %s", metric.Entity.Type, metric.Entity.Value)
		}
	}

	return metrics
}

func GetVmMetricsFromEntity(vc *HostConfig, ent performance.EntityMetric) ([]vMetric, error) {
	var metrics []vMetric

	vm := object.NewVirtualMachine(vc.PerfConnection.Client.Client, ent.Entity)
	name, err := vm.ObjectName(vc.PerfConnection.Context)
	moref := string(ent.Entity.Value)
	if err != nil {
		log.Error(err.Error())
		return metrics, err
	}

	// Get Host name
	h, err := vm.HostSystem(vc.PerfConnection.Context)
	if err != nil {
		log.Error(err.Error())
		return metrics, err
	}

	hr, err := HostSystemFromRef(vc.PerfConnection.Client, h.Reference())
	if err != nil {
		if false == strings.EqualFold(err.Error(), "Not an *object.HostSystem") {
			log.Error(err.Error())
			return metrics, err
		}
	}
	host := hr.Name()
	host = strings.ToLower(host)
	// Get host parent reference
	var hostparent mo.HostSystem
	err = h.Properties(vc.PerfConnection.Context, h.Reference(), []string{"parent"}, &hostparent)
	if err != nil {
		log.Error(err.Error())
		return metrics, err
	}

	// Get name of cluster the host is part of
	cls, err := ClusterFromRef(vc.PerfConnection.Client, hostparent.Parent.Reference())
	var cname string
	if err != nil {
		if false == strings.EqualFold(err.Error(), "Not an *object.ClusterComputeResource") {
			log.Error(err.Error())
			return metrics, err
		} else {
			cname = "standalone"
		}
	} else {
		cname = cls.Name()
		cname = strings.ToLower(cname)
	}

	for _, v := range ent.Value {
		for _, cn := range cfg.VmPerfCounters {
			if strings.EqualFold(cn.VName, v.Name) {
				if len(v.Value) > 0 {
					//log.Debugf("Storing VM metric %s with value %d", cn.PName, v.Value[0])
					metrics = append(metrics, vMetric{name: cn.PName, mtype: Gauge, help: cn.Help,
						value: float64(v.Value[0]), labels: map[string]string{"vcenter": vc.Host, "cluster": cname, "host": host, "vmname": name, "moref": moref, "minstance": v.Instance}})
				}
			}
		}
	}

	return metrics, nil
}

func GetHostMetricsFromEntity(vc *HostConfig, view *view.ContainerView,ent performance.EntityMetric) ([]vMetric, error) {
	var metrics []vMetric

	host := object.NewHostSystem(vc.PerfConnection.Client.Client, ent.Entity)
	name, err := host.ObjectName(vc.PerfConnection.Context)
	if err != nil {
		log.Error(err.Error())
		return metrics, err
	}
	var h []mo.HostSystem
	err = view.RetrieveWithFilter(vc.PerfConnection.Context, []string{"HostSystem"}, []string{"name", "parent"}, &h, property.Filter{"name": name})
	if err != nil {
		log.Error(err.Error())
		return metrics, err
	}
	if len(h) != 1 {
		log.Errorf("HostSystem not found: %s", name)
		return metrics, errors.New("HostSystem not found")
	}
	// Get name of cluster the host is part of
	cls, err := ClusterFromRef(vc.PerfConnection.Client, h[0].Parent.Reference())
	var cname string
	if err != nil {
		if false == strings.EqualFold(err.Error(), "Not an *object.ClusterComputeResource") {
			log.Error(err.Error())
			return metrics, err
		} else {
			cname = "standalone"
		}
	} else {
		cname = cls.Name()
		cname = strings.ToLower(cname)
	}

	for _, v := range ent.Value {
		for _, cn := range cfg.HostPerfCounters {
			if strings.EqualFold(cn.VName, v.Name) {
				if len(v.Value) > 0 {
					//log.Debugf("Storing Host metric %s with value %d", cn.PName, v.Value[0])
					metrics = append(metrics, vMetric{name: cn.PName, mtype: Gauge, help: cn.Help,
						value: float64(v.Value[0]), labels: map[string]string{"vcenter": vc.Host, "cluster": cname, "host": name, "minstance": v.Instance}})
				}
			}
		}
	}

	return metrics, nil
}

func DSMetrics(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	vmgr, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"ClusterComputeResource"}, true)
	if err != nil {
		log.Error(err.Error())
	}

	defer vmgr.Destroy(ctx)

	var lst []mo.ClusterComputeResource
	err = vmgr.Retrieve(ctx, []string{"ClusterComputeResource"}, []string{"name", "datastore"}, &lst)
	if err != nil {
		log.Error(err.Error())
	}

	for _, cls := range lst {
		vcname := vc.Host
		cname := cls.Name
		cname = strings.ToLower(cname)

		var dsl []mo.Datastore
		pc := c.PropertyCollector()
		pc.Retrieve(ctx, cls.Datastore, []string{"summary", "name"}, &dsl)

		for _, ds := range dsl {
			if ds.Summary.Accessible {

				ds_capacity := ds.Summary.Capacity / 1024 / 1024 / 1024
				ds_freespace := ds.Summary.FreeSpace / 1024 / 1024 / 1024
				ds_used := ds_capacity - ds_freespace
				ds_pused := math.Round((float64(ds_used) / float64(ds_capacity)) * 100)
				ds_uncommitted := ds.Summary.Uncommitted / 1024 / 1024 / 1024
				ds_name := ds.Summary.Name

				metrics = append(metrics, vMetric{name: "vsphere_datastore_capacity_size", mtype: Gauge, help: "Datastore Total Size in GB",
					value: float64(ds_capacity), labels: map[string]string{"vcenter": vcname, "datastore": ds_name, "cluster": cname}})
				metrics = append(metrics, vMetric{name: "vsphere_datastore_capacity_free", mtype: Gauge, help: "Datastore Size Free in GB",
					value: float64(ds_freespace), labels: map[string]string{"vcenter": vcname, "datastore": ds_name, "cluster": cname}})
				metrics = append(metrics, vMetric{name: "vsphere_datastore_capacity_used", mtype: Gauge, help: "Datastore Size Used in GB",
					value: float64(ds_used), labels: map[string]string{"vcenter": vcname, "datastore": ds_name, "cluster": cname}})
				metrics = append(metrics, vMetric{name: "vsphere_datastore_capacity_uncommitted", mtype: Gauge, help: "Datastore Size Uncommitted in GB",
					value: float64(ds_uncommitted), labels: map[string]string{"vcenter": vcname, "datastore": ds_name, "cluster": cname}})
				metrics = append(metrics, vMetric{name: "vsphere_datastore_capacity_pused", mtype: Gauge, help: "Datastore Size in percent",
					value: ds_pused, labels: map[string]string{"vcenter": vcname, "datastore": ds_name, "cluster": cname}})
			}

		}
	}

	return metrics
}

func ClusterMetrics(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)

	var clusters []mo.ClusterComputeResource
	e2 := GetClusters(ctx, c, &clusters)
	if e2 != nil {
		log.Error(e2.Error())
	}

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"ResourcePool"}, true)
	if err != nil {
		log.Error(err.Error())
	}

	defer v.Destroy(ctx)

	var pools []mo.ResourcePool
	err = v.RetrieveWithFilter(ctx, []string{"ResourcePool"}, []string{"summary", "name", "parent", "config"}, &pools, property.Filter{"name": "Resources"})
	if err != nil {
		log.Error(err.Error())
		//return err
	}

	for _, pool := range pools {
		if pool.Summary != nil {
			// Get Cluster name from Resource Pool Parent
			cls, err := ClusterFromID(c, pool.Parent.Value)
			if err != nil {
				log.Error(err.Error())
				return nil
			}

			vcname := vc.Host
			cname := cls.Name()
			cname = strings.ToLower(cname)

			// Get Quickstats form Resource Pool
			qs := pool.Summary.GetResourcePoolSummary().QuickStats
			memLimit := pool.Config.MemoryAllocation.Limit

			// Memory
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_ballooned", mtype: Gauge, help: "Cluster Memory Ballooned",
				value: float64(qs.BalloonedMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_compressed", mtype: Gauge, help: "The amount of compressed memory currently consumed by VM",
				value: float64(qs.CompressedMemory / 1024 / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_consumedOverhead", mtype: Gauge, help: "The amount of overhead memory",
				value: float64(qs.ConsumedOverheadMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_distributedMemoryEntitlement", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.DistributedMemoryEntitlement / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_guest", mtype: Gauge, help: "Guest memory utilization statistics",
				value: float64(qs.GuestMemoryUsage / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_private", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.PrivateMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_staticMemoryEntitlement", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.StaticMemoryEntitlement / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_shared", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.SharedMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_swapped", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.SwappedMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_limit", mtype: Gauge, help: "Cluster Memory ",
				value: float64(*memLimit / 1024 / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_usage", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.HostMemoryUsage / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_overhead", mtype: Gauge, help: "Cluster Memory ",
				value: float64(qs.OverheadMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})

			// CPU
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_distributedCpuEntitlement", mtype: Gauge, help: "Cluster CPU, MHz ",
				value: float64(qs.DistributedCpuEntitlement), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_demand", mtype: Gauge, help: "Cluster CPU demand, MHz",
				value: float64(qs.OverallCpuDemand), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_usage", mtype: Gauge, help: "Cluster CPU usage MHz",
				value: float64(qs.OverallCpuUsage), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_staticCpuEntitlement", mtype: Gauge, help: "Cluster CPU static, MHz",
				value: float64(qs.StaticCpuEntitlement), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_limit", mtype: Gauge, help: "Cluster CPU, MHz ",
				value: float64(*pool.Config.CpuAllocation.Limit), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
		}
	}

	for _, cl := range clusters {
		if cl.Summary != nil {
			vcname := vc.Host
			cname := cl.Name
			cname = strings.ToLower(cname)
			qs := cl.Summary.GetComputeResourceSummary()

			// Memory
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_effective", mtype: Gauge, help: "Effective amount of Memory in Cluster in GB",
				value: float64(qs.EffectiveMemory / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_mem_total", mtype: Gauge, help: "Total Amount of Memory in Cluster in GB",
				value: float64(qs.TotalMemory / 1024 / 1024 / 1024), labels: map[string]string{"vcenter": vcname, "cluster": cname}})

			// CPU
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_effective", mtype: Gauge, help: "Effective available CPU Hz in Cluster",
				value: float64(qs.EffectiveCpu), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_cpu_total", mtype: Gauge, help: "Total Amount of CPU Hz in Cluster",
				value: float64(qs.TotalCpu), labels: map[string]string{"vcenter": vcname, "cluster": cname}})

			// Misc
			metrics = append(metrics, vMetric{name: "vsphere_cluster_numHosts", mtype: Gauge, help: "Number of Hypervisors in cluster",
				value: float64(qs.NumHosts), labels: map[string]string{"vcenter": vcname, "cluster": cname}})

			// Virtual Servers, powered on vs created in cluster
			v, err := m.CreateContainerView(ctx, cl.Reference(), []string{"VirtualMachine"}, true)
			if err != nil {
				log.Error(err.Error())
			}

			var vms []mo.VirtualMachine

			err = v.RetrieveWithFilter(ctx, []string{"VirtualMachine"}, []string{"summary", "parent"}, &vms, property.Filter{"runtime.powerState": "poweredOn"})
			if err != nil {
				log.Error(err.Error())
			}
			poweredOn := len(vms)

			err = v.Retrieve(ctx, []string{"VirtualMachine"}, []string{"summary", "parent"}, &vms)
			if err != nil {
				log.Error(err.Error())
			}
			total := len(vms)

			metrics = append(metrics, vMetric{name: "vsphere_cluster_vm_poweredon", mtype: Gauge, help: "Number of vms running in cluster",
				value: float64(poweredOn), labels: map[string]string{"vcenter": vcname, "cluster": cname}})
			metrics = append(metrics, vMetric{name: "vsphere_cluster_vm_total", mtype: Gauge, help: "Number of vms in cluster",
				value: float64(total), labels: map[string]string{"vcenter": vcname, "cluster": cname}})

		}

	}

	return metrics
}

func ClusterCounters(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"ClusterComputeResource"}, true)
	if err != nil {
		log.Error(err.Error())

	}

	defer v.Destroy(ctx)

	var lst []mo.ClusterComputeResource
	err = v.Retrieve(ctx, []string{"ClusterComputeResource"}, []string{"name"}, &lst)
	if err != nil {
		log.Error(err.Error())

	}

	pm := performance.NewManager(c.Client)
	mlist, err := pm.CounterInfoByKey(ctx)
	if err != nil {
		log.Error(err.Error())

	}

	for _, cls := range lst {
		cname := cls.Name
		cname = strings.ToLower(cname)

		am, _ := pm.AvailableMetric(ctx, cls.Reference(), 300)

		var pqList []types.PerfMetricId
		for _, v := range am {

			if strings.Contains(mlist[v.CounterId].Name(), "vmop") {
				pqList = append(pqList, v)
			}
		}

		querySpec := types.PerfQuerySpec{
			Entity:     cls.Reference(),
			MetricId:   pqList,
			MaxSample:  1,
			IntervalId: 300,
		}
		query := types.QueryPerf{
			This:      pm.Reference(),
			QuerySpec: []types.PerfQuerySpec{querySpec},
		}

		response, err := methods.QueryPerf(ctx, c, &query)
		// With multi vcenter support, connection error should not be fatal anymore
		if err != nil {
			//log.Fatal(err)
			log.Error(err)
			return metrics
		}

		for _, base := range response.Returnval {
			metric := base.(*types.PerfEntityMetric)
			for _, baseSeries := range metric.Value {
				series := baseSeries.(*types.PerfMetricIntSeries)
				name := strings.TrimLeft(mlist[series.Id.CounterId].Name(), "vmop.")
				name = strings.TrimRight(name, ".latest")
				metrics = append(metrics, vMetric{name: "vsphere_cluster_vmop_" + name, mtype: Counter, help: "vmops counter ",
					value: float64(series.Value[0]), labels: map[string]string{"vcenter": vc.Host, "cluster": cname}})

			}
		}

	}
	return metrics
}

// Collects Hypervisor metrics
func HostMetrics(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric
	var cname string

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		log.Error(err.Error())
	}

	defer v.Destroy(ctx)

	var hosts []mo.HostSystem
	err = v.Retrieve(ctx, []string{"HostSystem"}, []string{"summary", "parent", "vm"}, &hosts)
	if err != nil {
		log.Error(err.Error())
	}

	for _, hs := range hosts {
		// Get name of cluster the host is part of (else use "standalone" as cluster name)
		cls, err := ClusterFromRef(c, hs.Parent.Reference())
		if err != nil {
			if false == strings.EqualFold(err.Error(), "Not an *object.ClusterComputeResource") {
				log.Error(err.Error())
				return nil
			} else {
				cname = "standalone"
			}
		} else {
			cname = cls.Name()
			cname = strings.ToLower(cname)
		}

		vcname := vc.Host
		name := hs.Summary.Config.Name

		totalCPU := int64(hs.Summary.Hardware.CpuMhz) * int64(hs.Summary.Hardware.NumCpuCores)
		freeCPU := int64(totalCPU) - int64(hs.Summary.QuickStats.OverallCpuUsage)
		cpuPusage := math.Round((float64(hs.Summary.QuickStats.OverallCpuUsage) / float64(totalCPU)) * 100)

		totalMemory := float64(hs.Summary.Hardware.MemorySize / 1024 / 1024 / 1024)
		usedMemory := float64(hs.Summary.QuickStats.OverallMemoryUsage / 1024)
		freeMemory := totalMemory - usedMemory
		memPusage := math.Round((usedMemory / totalMemory) * 100)

		metrics = append(metrics, vMetric{name: "vsphere_host_cpu_usage", mtype: Gauge, help: "Hypervisors CPU usage",
			value: float64(hs.Summary.QuickStats.OverallCpuUsage), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_cpu_total", mtype: Gauge, help: "Hypervisors CPU Total",
			value: float64(totalCPU), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_cpu_free", mtype: Gauge, help: "Hypervisors CPU Free",
			value: float64(freeCPU), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_cpu_pusage", mtype: Gauge, help: "Hypervisors CPU Percent Usage",
			value: float64(cpuPusage), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})

		metrics = append(metrics, vMetric{name: "vsphere_host_mem_usage", mtype: Gauge, help: "Hypervisors Memory Usage in GB",
			value: usedMemory, labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_mem_total", mtype: Gauge, help: "Hypervisors Memory Total in GB",
			value: totalMemory, labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_mem_free", mtype: Gauge, help: "Hypervisors Memory Free in GB",
			value: float64(freeMemory), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_mem_pusage", mtype: Gauge, help: "Hypervisors Memory Percent Usage",
			value: float64(memPusage), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})

	}

	return metrics
}

// Collects Hypervisor counters
func HostCounters(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric
	var cname string

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}

	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		log.Error(err.Error() + ": HostCounters")
	}

	defer v.Destroy(ctx)

	var hosts []mo.HostSystem
	err = v.Retrieve(ctx, []string{"HostSystem"}, []string{"name", "parent", "summary"}, &hosts)
	if err != nil {
		log.Error(err.Error() + ": HostCounters")
	}

	for _, hs := range hosts {
		// Get name of cluster the host is part of
		cls, err := ClusterFromRef(c, hs.Parent.Reference())
		if err != nil {
			if false == strings.EqualFold(err.Error(), "Not an *object.ClusterComputeResource") {
				log.Error(err.Error())
				return nil
			} else {
				cname = "standalone"
			}
		} else {
			cname = cls.Name()
			cname = strings.ToLower(cname)
		}

		vcname := vc.Host
		name := hs.Summary.Config.Name

		vMgr := view.NewManager(c.Client)
		vmView, err := vMgr.CreateContainerView(ctx, hs.Reference(), []string{"VirtualMachine"}, true)
		if err != nil {
			log.Error(err.Error() + " " + hs.Name)
		}

		var vms []mo.VirtualMachine

		err2 := vmView.RetrieveWithFilter(ctx, []string{"VirtualMachine"}, []string{"name", "runtime"}, &vms, property.Filter{"runtime.powerState": "poweredOn"})
		if err2 != nil {
			//	log.Error(err2.Error() +": HostCounters - poweron")
		}

		poweredOn := len(vms)

		err = vmView.Retrieve(ctx, []string{"VirtualMachine"}, []string{"name", "summary.config", "runtime.powerState"}, &vms)
		if err != nil {
			log.Error(err.Error() + " : " + "in retrieving vms")
		}

		total := len(vms)

		metrics = append(metrics, vMetric{name: "vsphere_host_vm_poweron", mtype: Gauge, help: "Number of vms running on host",
			value: float64(poweredOn), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_vm_total", mtype: Gauge, help: "Number of vms registered on host",
			value: float64(total), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})

		var vMem int64
		var vCPU int64
		var vCPUOn int64
		var vMemOn int64
		vCPU = 0
		vMem = 0
		vCPUOn = 0
		vMemOn = 0

		for _, vm := range vms {

			vCPU = vCPU + int64(vm.Summary.Config.NumCpu)
			vMem = vMem + int64(vm.Summary.Config.MemorySizeMB/1024)

			pwr := string(vm.Runtime.PowerState)
			//fmt.Println(pwr)
			if pwr == "poweredOn" {
				vCPUOn = vCPUOn + int64(vm.Summary.Config.NumCpu)
				vMemOn = vMemOn + int64(vm.Summary.Config.MemorySizeMB/1024)
			}
		}

		metrics = append(metrics, vMetric{name: "vsphere_host_vcpu_all", mtype: Gauge, help: "Number of vcpu configured on host",
			value: float64(vCPU), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_vmem_all", mtype: Gauge, help: "Total vmem configured on host in GB",
			value: float64(vMem), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_vcpu_on", mtype: Gauge, help: "Number of vcpu configured and running on host",
			value: float64(vCPUOn), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})
		metrics = append(metrics, vMetric{name: "vsphere_host_vmem_on", mtype: Gauge, help: "Total vmem configured and running on host in GB",
			value: float64(vMemOn), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname}})

		cores := hs.Summary.Hardware.NumCpuCores
		model := hs.Summary.Hardware.CpuModel

		metrics = append(metrics, vMetric{name: "vsphere_host_cores", mtype: Gauge, help: "Number of physical cores available on host",
			value: float64(cores), labels: map[string]string{"vcenter": vcname, "host": name, "cluster": cname, "cpumodel": model}})

		vmView.Destroy(ctx)
	}

	return metrics
}

// Report status of the HBA attached to a hypervisor to be able to monitor if a hba goes offline
func HostHBAStatus(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric
	var cname string

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}
	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		log.Error(err.Error())
	}
	defer v.Destroy(ctx)

	var hosts []mo.HostSystem
	err = v.Retrieve(ctx, []string{"HostSystem"}, []string{"name", "parent"}, &hosts)
	if err != nil {
		log.Error(err.Error())
	}

	for _, host := range hosts {
		// Get name of cluster the host is part of
		cls, err := ClusterFromRef(c, host.Parent.Reference())
		if err != nil {
			if false == strings.EqualFold(err.Error(), "Not an *object.ClusterComputeResource") {
				log.Error(err.Error())
				return nil
			} else {
				cname = "standalone"
			}
		} else {
			cname = cls.Name()
			cname = strings.ToLower(cname)
		}

		vcname := vc.Host

		hcm := object.NewHostConfigManager(c.Client, host.Reference())
		ss, err := hcm.StorageSystem(ctx)
		if err != nil {
			log.Error(err.Error())
		}

		var hss mo.HostStorageSystem
		err = ss.Properties(ctx, ss.Reference(), []string{"StorageDeviceInfo.HostBusAdapter"}, &hss)
		if err != nil {
			return nil
		}

		hbas := hss.StorageDeviceInfo.HostBusAdapter

		for _, v := range hbas {

			hba := v.GetHostHostBusAdapter()

			if hba.Status != "unknown" {
				status := 0
				if hba.Status == "online" {
					status = 1
				}
				metrics = append(metrics, vMetric{name: "vsphere_host_hba_status", mtype: Gauge, help: "Hypervisors hba Online status, 1 == Online",
					value: float64(status), labels: map[string]string{"vcenter": vcname, "host": host.Name, "cluster": cname, "hba": hba.Device}})
			}

		}
	}

	return metrics
}

func VmMetrics(vc HostConfig) []vMetric {
	defer recoverCollector()
	log.SetReportCaller(true)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout)
	defer cancel()

	var metrics []vMetric

	c, err := NewClient(vc, ctx)
	// With multi vcenter support, connection error should not be fatal anymore
	if err != nil {
		//log.Fatal(err)
		log.Error(err)
		return metrics
	}
	defer c.Logout(ctx)

	m := view.NewManager(c.Client)
	defer m.Destroy(ctx)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		log.Error(err.Error())
	}
	defer v.Destroy(ctx)

	var vms []mo.VirtualMachine

	err = v.Retrieve(ctx, []string{"VirtualMachine"}, []string{"summary", "config", "name"}, &vms)
	if err != nil {
		log.Error(err.Error())
	}

	for _, vm := range vms {
		// Get Host name
		h, err := HostSystemFromRef(c, vm.Summary.Runtime.Host.Reference())
		if err != nil {
			if false == strings.EqualFold(err.Error(), "Not an *object.HostSystem") {
				log.Error(err.Error())
				return nil
			}
		}
		host := h.Name()
		host = strings.ToLower(host)

		// VM Memory
		freeMemory := (int64(vm.Summary.Config.MemorySizeMB) * 1024 * 1024) - (int64(vm.Summary.QuickStats.GuestMemoryUsage) * 1024 * 1024)
		BalloonedMemory := int64(vm.Summary.QuickStats.BalloonedMemory) * 1024 * 1024
		GuestMemoryUsage := int64(vm.Summary.QuickStats.GuestMemoryUsage) * 1024 * 1024
		VmMemory := int64(vm.Config.Hardware.MemoryMB) * 1024 * 1024

		metrics = append(metrics, vMetric{name: "vsphere_vm_mem_total", mtype: Gauge, help: "VM Memory total, Byte",
			value: float64(VmMemory), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})
		metrics = append(metrics, vMetric{name: "vsphere_vm_mem_free", mtype: Gauge, help: "VM Memory total, Byte",
			value: float64(freeMemory), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})
		metrics = append(metrics, vMetric{name: "vsphere_vm_mem_usage", mtype: Gauge, help: "VM Memory usage, Byte",
			value: float64(GuestMemoryUsage), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})
		metrics = append(metrics, vMetric{name: "vsphere_vm_mem_balloonede", mtype: Gauge, help: "VM Memory Ballooned, Byte",
			value: float64(BalloonedMemory), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})

		metrics = append(metrics, vMetric{name: "vsphere_vm_cpu_usage", mtype: Gauge, help: "VM CPU Usage, MHz",
			value: float64(vm.Summary.QuickStats.OverallCpuUsage), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})
		metrics = append(metrics, vMetric{name: "vsphere_vm_cpu_demand", mtype: Gauge, help: "VM CPU Demand, MHz",
			value: float64(vm.Summary.QuickStats.OverallCpuDemand), labels: map[string]string{"vcenter": vc.Host, "host": host, "vmname": vm.Name, "moref": vm.Summary.Vm.Value}})
	}

	return metrics
}


func GetClusters(ctx context.Context, c *govmomi.Client, lst *[]mo.ClusterComputeResource) error {
	defer recoverCollector()

	m := view.NewManager(c.Client)

	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"ClusterComputeResource"}, true)
	if err != nil {
		log.Error(err.Error())
		return err
	}
	defer v.Destroy(ctx)

	err = v.Retrieve(ctx, []string{"ClusterComputeResource"}, []string{"name", "summary"}, lst)
	if err != nil {
		log.Error(err.Error())
		return err
	}

	return nil
}

// ClusterFromID returns a ClusterComputeResource, a subclass of
// ComputeResource that is used for clusters.
func ClusterFromID(client *govmomi.Client, id string) (*object.ClusterComputeResource, error) {
	defer recoverCollector()
	finder := find.NewFinder(client.Client, false)

	ref := types.ManagedObjectReference{
		Type:  "ClusterComputeResource",
		Value: id,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	obj, err := finder.ObjectReference(ctx, ref)
	if err != nil {
		return nil, err
	}
	return obj.(*object.ClusterComputeResource), nil
}

func ClusterFromRef(client *govmomi.Client, ref types.ManagedObjectReference) (*object.ClusterComputeResource, error) {
	defer recoverCollector()
	finder := find.NewFinder(client.Client, false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	obj, err := finder.ObjectReference(ctx, ref)
	if err != nil {
		return nil, err
	}

	switch obj.(type) {
	case *object.ClusterComputeResource:
		return obj.(*object.ClusterComputeResource), nil
	default:
		//log.Debugf("This is NOT *object.ClusterComputeResource: %T", obj)
		return nil, errors.New("Not an *object.ClusterComputeResource")
	}

	// We wont never reach this
	return nil, nil
}

func HostSystemFromRef(client *govmomi.Client, ref types.ManagedObjectReference) (*object.HostSystem, error) {
	defer recoverCollector()
	finder := find.NewFinder(client.Client, false)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	obj, err := finder.ObjectReference(ctx, ref)
	if err != nil {
		return nil, err
	}

	switch obj.(type) {
	case *object.HostSystem:
		return obj.(*object.HostSystem), nil
	default:
		//log.Debugf("This is NOT *object.HostSystem: %T", obj)
		return nil, errors.New("Not an *object.HostSystem")
	}

	// We wont never reach this
	return nil, nil
}
