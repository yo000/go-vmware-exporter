package main

import (
	"flag"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"time"
)

type HostConfig struct {
	Host     string `yaml:"host"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

type Configuration struct {
	Hosts        []HostConfig `yaml:"hosts"`
	Debug        bool         `yaml:"debug"`
	VmStats      bool         `yaml:"vmstats"`
	ClusterStats bool         `yaml:"clusterstats"`
}

var (
	cfg     Configuration
	cfgFile *string

	defaultTimeout time.Duration
)

func main() {
	port := flag.Int("port", 9094, "Port to attach exporter")
	cfgFile = flag.String("config", "config.yaml", "config file")
	flag.Parse()

	loadConfig()

	if cfg.Debug == true {
		log.SetLevel(log.DebugLevel)
		log.Debugf("debug=%v\n", cfg.Debug)
		log.Debugf("vmstats=%v\n", cfg.VmStats)
		log.Debugf("clusterstats=%v\n", cfg.ClusterStats)
	}

	prometheus.MustRegister(NewvCollector())

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

	// Some big loaded vcenters take much time to return cluster counters
	defaultTimeout = 60 * time.Second

	// Get config details from config file
	f, err := ioutil.ReadFile(*cfgFile)
	if err != nil {
		log.Fatal(err)
	}

	err = yaml.Unmarshal(f, &cfg)
	if err != nil {
		log.Fatal(err)
	}

	// if env var defined, then it overload config file
	if os.Getenv("HOST") != "" && os.Getenv("USERID") != "" && os.Getenv("PASSWORD") != "" {
		cfg = Configuration{}
		h := HostConfig{Host: os.Getenv("HOST"), User: os.Getenv("USERID"), Password: os.Getenv("PASSWORD")}
		cfg.Hosts = append(cfg.Hosts, h)

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
	}

}
