# Prometheus vmware exporter in golang

**Up and running in 3 Steps**

1 - Build:
```
$ git clone https://github.com/marstid/go-vmware-exporter.git
$ cd go-vmware-exporter/
$ docker build -t go-vm -f Dockerfile .
```

2 - Edit docker-compose.yml to configure vCenter host and credentials.
```
$ vi docker-compose.yml
```

3 - Start 
```
$ docker-compose up -d
```


Curl http://localhost:9094


# Supported Metrics



Check if host HBA is Online or Offline
vsphere_host_hba_status{cluster="clustername",hba="vmhba1",host="hypervisor.host.name"} 1.0`

You can specify VM and Host Performance counters in config file config.yaml :
```
vmperfcounters:
  - vname: 'disk.usage.average'
    pname: 'vsphere_vm_disk_usage_avg'
    help: 'Averafe use rate in kilobytes per second'

hostperfcounters:
  - vname: 'power.power.average'
    pname: 'vsphere_host_power_avg'
    help: 'Average host power use in watts'
```

See https://docs.vmware.com/en/vRealize-Operations/8.10/com.vmware.vcom.metrics.doc/GUID-C3CAAE15-2E83-431F-8F2D-C5297A3B6EA9.html for host metrics
https://docs.vmware.com/en/vRealize-Operations/8.10/com.vmware.vcom.metrics.doc/GUID-1322F5A4-DA1D-481F-BBEA-99B228E96AF2.html for vm metrics
