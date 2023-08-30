package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/vmware/govmomi"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"context"
	"errors"
	"time"
	"flag"
	"os"
)

type HostConfig struct {
	Host     string `yaml:"host"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	StdConnection   HostConnection
	PerfConnection  HostConnection
	StdMetrics      []vMetric
	PerfMetrics     []vMetric
}

type HostConnection struct {
	Context        context.Context
	CancelContext  func()
	Client         *govmomi.Client
}

type PerfCounter struct {
	// Name in vmware
	VName string `yaml:"vname"`
	// Name of prometheus metric
	PName string `yaml:"pname"`
	// Help in prometheus
	Help string `yaml:"help"`
}

type Configuration struct {
	Hosts               []HostConfig  `yaml:"hosts"`
	Debug               bool          `yaml:"debug"`
	VmStats             bool          `yaml:"vmstats"`
	ClusterStats        bool          `yaml:"clusterstats"`
	VmPerfCounters      []PerfCounter `yaml:"vmperfcounters"`
	HostPerfCounters    []PerfCounter `yaml:"hostperfcounters"`
	ReqPerfTimeoutSec   int           `yaml:"perftimeout"`
	ReqPerfTimeout      time.Duration
	ReqTimeoutSec       int           `yaml:"timeout"`
	ReqTimeout          time.Duration
}

var (
	cfg       Configuration
	cfgFile   *string
	StartTime int64
)

func getCfgVcHostFromName(vchost string) (*HostConfig, error) {
	for i, vc := range cfg.Hosts {
		if strings.EqualFold(vc.Host, vchost) {
			return &cfg.Hosts[i], nil
		}
	}
	return &HostConfig{}, errors.New("Not found")
}

func main() {
	var err error
	log.SetReportCaller(true)

	StartTime = time.Now().Unix()

	port := flag.Int("port", 9094, "Port to attach exporter")
	cfgFile = flag.String("config", "config.yaml", "config file")
	flag.Parse()

	loadConfig()

	if cfg.Debug == true {
		log.SetLevel(log.DebugLevel)
		log.Debugf("debug=%v\n", cfg.Debug)
		log.Debugf("vmstats=%v\n", cfg.VmStats)
		log.Debugf("clusterstats=%v\n", cfg.ClusterStats)
		log.Debugf("vmperfcounters=%v\n", cfg.VmPerfCounters)
		log.Debugf("hostperfcounters=%v\n", cfg.HostPerfCounters)
	}

	// Initialise VC connections
	for i, vc := range cfg.Hosts {
		// Pour l'instant std metrics conserve une connexion par thread
		/*
		log.Debugf("Open std metrics collect connection to %s", vc.Host)
		cfg.Hosts[i].StdConnection.Context, cfg.Hosts[i].StdConnection.CancelContext = context.WithTimeout(context.Background(), cfg.ReqTimeout)
		cfg.Hosts[i].StdConnection.Client, err = NewClient(vc, cfg.Hosts[i].StdConnection.Context)
		if err != nil {
			log.Error(err)
		}
		*/
		if len(cfg.VmPerfCounters) > 0 {
			log.Debugf("Open performance metrics collect connection to %s", vc.Host)
			cfg.Hosts[i].PerfConnection.Context, cfg.Hosts[i].PerfConnection.CancelContext = context.WithTimeout(context.Background(), cfg.ReqPerfTimeout)
			cfg.Hosts[i].PerfConnection.Client, err = NewClient(vc, cfg.Hosts[i].PerfConnection.Context)
			if err != nil {
				log.Error(err)
			}
		}
	}

	log.Info("Register collector")
	prometheus.MustRegister(NewvCollector())

	log.Info("Create HTTP router")
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", redirect)

	log.Info("Serving metrics on " + strconv.FormatInt(int64(*port), 10))
	log.Fatal(http.ListenAndServe(":"+strconv.FormatInt(int64(*port), 10), nil))
}

// Redirect
func redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/metrics", 301)
}

func loadConfig() {

	// Default value
	cfg.ReqTimeout = 60 * time.Second
	cfg.ReqPerfTimeout = 60 * time.Second

	// Get config details from config file
	f, err := ioutil.ReadFile(*cfgFile)
	if err != nil {
		log.Fatal(err)
	}

	err = yaml.Unmarshal(f, &cfg)
	if err != nil {
		log.Fatal(err)
	}
	// FIXME
	if cfg.ReqTimeoutSec > 0 {
		cfg.ReqTimeout = time.Duration(cfg.ReqTimeoutSec) * time.Second
	}
	if cfg.ReqPerfTimeoutSec > 0 {
		cfg.ReqPerfTimeout = time.Duration(cfg.ReqPerfTimeoutSec) * time.Second
	}

	// if env var defined, then it overload config file
	if os.Getenv("HOST") != "" && os.Getenv("USERID") != "" && os.Getenv("PASSWORD") != "" {
		cfg = Configuration{}
		h := HostConfig{Host: os.Getenv("HOST"), User: os.Getenv("USERID"), Password: os.Getenv("PASSWORD")}
		cfg.Hosts = append(cfg.Hosts, h)

		if len(os.Getenv("TIMEOUT")) > 0 {
			t, err := strconv.Atoi(os.Getenv("TIMEOUT"))
			if err != nil {
				log.Fatal("Invalid TIMEOUT value, should be an integer in seconds")
			}
			cfg.ReqTimeout = time.Duration(t) * time.Second
		}
		if len(os.Getenv("PERFTIMEOUT")) > 0 {
			t, err := strconv.Atoi(os.Getenv("PERFTIMEOUT"))
			if err != nil {
				log.Fatal("Invalid PERFTIMEOUT value, should be an integer in seconds")
			}
			cfg.ReqPerfTimeout = time.Duration(t) * time.Second
		}

		if os.Getenv("DEBUG") == "True" {
			cfg.Debug = true
		} else {
			cfg.Debug = false
		}
		if os.Getenv("VMSTATS") == "False" {
			cfg.VmStats = false
		} else {
			cfg.VmStats = true
		}
		if len(os.Getenv("VMPERFCOUNTERS")) > 0 {
			rawpc := os.Getenv("VMPERFCOUNTERS")
			pc := strings.Split(rawpc, ";")
			if len(pc)%3 != 0 {
				log.Fatal("Invalid VMPERFCOUNTERS value")
			}
			for i := 0; i < len(pc); i += 3 {
				cfg.VmPerfCounters = append(cfg.VmPerfCounters, PerfCounter{VName: pc[i], PName: pc[i+1], Help: pc[2]})
			}
		}
		if len(os.Getenv("HOSTPERFCOUNTERS")) > 0 {
			rawpc := os.Getenv("HOSTPERFCOUNTERS")
			pc := strings.Split(rawpc, ";")
			if len(pc)%3 != 0 {
				log.Fatal("Invalid HOSTPERFCOUNTERS value")
			}
			for i := 0; i < len(pc); i += 3 {
				cfg.HostPerfCounters = append(cfg.HostPerfCounters, PerfCounter{VName: pc[i], PName: pc[i+1], Help: pc[2]})
			}
		}
	}

}
