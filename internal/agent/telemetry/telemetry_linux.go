//go:build linux

package telemetry

import (
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/gleicon/fiia/internal/wire"
)

// Collector samples USE metrics from the host OS on each Collect() call.
// prev_* fields track the previous sample for delta-based metrics.
type Collector struct {
	prev_net  netSample
	prev_disk diskSample
	prev_time time.Time
}

// NewCollector creates a Collector. Call Collect() to sample metrics.
func NewCollector() *Collector { return &Collector{} }

var proc_buf_pool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

func readProcFile(path string) ([]byte, error) {
	bp := proc_buf_pool.Get().(*[]byte)
	defer proc_buf_pool.Put(bp)

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	n, err := f.Read(*bp)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	// Copy so caller holds data after buffer returns to pool.
	out := make([]byte, n)
	copy(out, (*bp)[:n])
	return out, nil
}

func readCPUSample() cpuSample {
	content, err := readProcFile("/proc/stat")
	if err != nil {
		return cpuSample{}
	}
	s, err := parseCPUStat(content)
	if err != nil {
		log.Printf("telemetry: parse /proc/stat: %v", err)
	}
	return s
}

func readPSIPressure(path string) float32 {
	content, err := readProcFile(path)
	if err != nil {
		// PSI may not exist on older kernels or in containers.
		return 0
	}
	return parsePSIPressure(content)
}

func readMemUtilSat() (util_pct, sat_pct float32) {
	content, err := readProcFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	total_kb, avail_kb, err := parseMemInfo(content)
	if err != nil {
		log.Printf("telemetry: parse /proc/meminfo: %v", err)
		return 0, 0
	}
	return memUtilPct(total_kb, avail_kb), readPSIPressure("/proc/pressure/memory")
}

func readNetSample() netSample {
	content, err := readProcFile("/proc/net/dev")
	if err != nil {
		return netSample{}
	}
	s, err := parseNetDev(content)
	if err != nil {
		log.Printf("telemetry: parse /proc/net/dev: %v", err)
	}
	return s
}

func readDiskSample() diskSample {
	content, err := readProcFile("/proc/diskstats")
	if err != nil {
		return diskSample{}
	}
	s, err := parseDiskStats(content)
	if err != nil {
		log.Printf("telemetry: parse /proc/diskstats: %v", err)
	}
	return s
}

// Collect samples current USE metrics. The first call returns zero for
// delta-based metrics (net BPS, disk util); subsequent calls compute deltas.
func (c *Collector) Collect() wire.USEMetrics {
	now := time.Now()

	cpu_first := readCPUSample()
	time.Sleep(200 * time.Millisecond)
	cpu_cur := readCPUSample()

	cpu_util := cpuUtilPct(cpu_first, cpu_cur)
	cpu_sat := readPSIPressure("/proc/pressure/cpu")

	mem_util, mem_sat := readMemUtilSat()

	net_cur := readNetSample()
	disk_cur := readDiskSample()

	elapsed_ms := now.Sub(c.prev_time).Milliseconds()
	elapsed_sec := now.Sub(c.prev_time).Seconds()

	disk_util := diskUtilPct(c.prev_disk, disk_cur, elapsed_ms)
	disk_sat := readPSIPressure("/proc/pressure/io")
	net_bps := netUtilBps(c.prev_net, net_cur, elapsed_sec)

	c.prev_net = net_cur
	c.prev_disk = disk_cur
	c.prev_time = now

	return wire.USEMetrics{
		CPUUtilPct:  cpu_util,
		CPUSatPct:   cpu_sat,
		MemUtilPct:  mem_util,
		MemSatPct:   mem_sat,
		DiskUtilPct: disk_util,
		DiskSatPct:  disk_sat,
		NetUtilBps:  net_bps,
		NetErrCount: net_cur.errors_total,
	}
}
