package metrics

import (
	"log"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gleicon/fiia/internal/hub/registry"
	"github.com/gleicon/fiia/internal/hub/store"
)

const (
	namespace = "fiia"
)

func assert(condition bool, message string) {
	if !condition {
		panic("hub/metrics: assertion failed: " + message)
	}
}

// Server exposes Prometheus metrics for the Fiia fleet at /metrics.
type Server struct {
	reg         *registry.Registry
	prom_gather prometheus.Gatherer
	nodes_alive prometheus.GaugeFunc
	nodes_total prometheus.GaugeFunc
}

// nodeMetricsCollector emits per-node USE metrics on each /metrics scrape.
type nodeMetricsCollector struct {
	reg          *registry.Registry
	desc_cpu_util  *prometheus.Desc
	desc_cpu_sat   *prometheus.Desc
	desc_mem_util  *prometheus.Desc
	desc_mem_sat   *prometheus.Desc
	desc_disk_util *prometheus.Desc
	desc_disk_sat  *prometheus.Desc
	desc_net_bps   *prometheus.Desc
	desc_net_errs  *prometheus.Desc
}

func newNodeMetricsCollector(reg *registry.Registry) *nodeMetricsCollector {
	assert(reg != nil, "registry must not be nil")
	label_names := []string{"node_id"}
	return &nodeMetricsCollector{
		reg:          reg,
		desc_cpu_util:  prometheus.NewDesc(namespace+"_node_cpu_util_pct",  "CPU utilization pct.", label_names, nil),
		desc_cpu_sat:   prometheus.NewDesc(namespace+"_node_cpu_sat_pct",   "CPU saturation PSI avg60.", label_names, nil),
		desc_mem_util:  prometheus.NewDesc(namespace+"_node_mem_util_pct",  "Memory utilization pct.", label_names, nil),
		desc_mem_sat:   prometheus.NewDesc(namespace+"_node_mem_sat_pct",   "Memory saturation PSI avg60.", label_names, nil),
		desc_disk_util: prometheus.NewDesc(namespace+"_node_disk_util_pct", "Disk utilization pct.", label_names, nil),
		desc_disk_sat:  prometheus.NewDesc(namespace+"_node_disk_sat_pct",  "Disk saturation PSI avg60.", label_names, nil),
		desc_net_bps:   prometheus.NewDesc(namespace+"_node_net_util_bps",  "Network utilization bytes/s.", label_names, nil),
		desc_net_errs:  prometheus.NewDesc(namespace+"_node_net_err_total", "Cumulative network errors.", label_names, nil),
	}
}

func (c *nodeMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	assert(ch != nil, "ch must not be nil")
	ch <- c.desc_cpu_util
	ch <- c.desc_cpu_sat
	ch <- c.desc_mem_util
	ch <- c.desc_mem_sat
	ch <- c.desc_disk_util
	ch <- c.desc_disk_sat
	ch <- c.desc_net_bps
	ch <- c.desc_net_errs
}

func (c *nodeMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	assert(ch != nil, "ch must not be nil")
	nodes := c.reg.GetAll()
	for _, n := range nodes {
		m := n.Metrics
		ch <- prometheus.MustNewConstMetric(c.desc_cpu_util,  prometheus.GaugeValue,   float64(m.CPUUtilPct),  n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_cpu_sat,   prometheus.GaugeValue,   float64(m.CPUSatPct),   n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_mem_util,  prometheus.GaugeValue,   float64(m.MemUtilPct),  n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_mem_sat,   prometheus.GaugeValue,   float64(m.MemSatPct),   n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_disk_util, prometheus.GaugeValue,   float64(m.DiskUtilPct), n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_disk_sat,  prometheus.GaugeValue,   float64(m.DiskSatPct),  n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_net_bps,   prometheus.GaugeValue,   float64(m.NetUtilBps),  n.NodeID)
		ch <- prometheus.MustNewConstMetric(c.desc_net_errs,  prometheus.CounterValue, float64(m.NetErrCount), n.NodeID)
	}
}

// New registers Prometheus gauges and returns a metrics Server.
// drift_counter and s are used for the drift_events_total and nodes_paused gauges.
// prom_reg/prom_gather are prometheus.DefaultRegisterer/Gatherer in production;
// tests pass an isolated prometheus.NewRegistry() for both.
func New(reg *registry.Registry, s store.Store, drift_counter *atomic.Int64,
	prom_reg prometheus.Registerer, prom_gather prometheus.Gatherer) *Server {
	assert(reg != nil, "registry must not be nil")
	assert(prom_reg != nil, "prom_reg must not be nil")
	assert(prom_gather != nil, "prom_gather must not be nil")

	factory := promauto.With(prom_reg)
	srv := &Server{reg: reg, prom_gather: prom_gather}

	srv.nodes_alive = factory.NewGaugeFunc(
		prometheus.GaugeOpts{Namespace: namespace, Name: "nodes_alive_total",
			Help: "Nodes with a heartbeat within the last 10 minutes."},
		func() float64 { return float64(reg.AliveCount()) },
	)
	srv.nodes_total = factory.NewGaugeFunc(
		prometheus.GaugeOpts{Namespace: namespace, Name: "nodes_total",
			Help: "Total nodes known to the hub."},
		func() float64 { return float64(reg.TotalCount()) },
	)
	if drift_counter != nil {
		factory.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: namespace, Name: "drift_events_total",
				Help: "Total drift events received since hub start."},
			func() float64 { return float64(drift_counter.Load()) },
		)
	}
	if s != nil {
		factory.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: namespace, Name: "nodes_paused",
				Help: "Nodes flagged AGENT_PAUSED (one missed heartbeat window)."},
			func() float64 {
				n, _ := s.CountNodesWithStatus("AGENT_PAUSED")
				return float64(n)
			},
		)
		factory.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: namespace, Name: "nodes_unreachable",
				Help: "Nodes flagged AGENT_UNREACHABLE (two+ missed heartbeat windows)."},
			func() float64 {
				n, _ := s.CountNodesWithStatus("AGENT_UNREACHABLE")
				return float64(n)
			},
		)
		factory.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: namespace, Name: "nodes_uninstrumented",
				Help: "Nodes in inventory but never reported a heartbeat."},
			func() float64 {
				n, _ := s.CountNodesWithStatus("UNINSTRUMENTED_SERVER")
				return float64(n)
			},
		)
	}

	prom_reg.MustRegister(newNodeMetricsCollector(reg))
	return srv
}

// Serve creates a TCP listener on addr and calls ServeListener.
func (s *Server) Serve(addr string) error {
	assert(addr != "", "addr must not be empty")
	assert(s.reg != nil, "registry must not be nil")

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("metrics: serving on %s", addr)
	return s.ServeListener(ln)
}

// ServeListener serves /metrics and /healthz on the given listener.
func (s *Server) ServeListener(ln net.Listener) error {
	assert(ln != nil, "ln must not be nil")

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.prom_gather, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return http.Serve(ln, mux)
}
