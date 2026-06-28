package telemetry

import (
	"bytes"
	"fmt"
	"strconv"
)

// parseCPUStat extracts the aggregate CPU line from /proc/stat content.
func parseCPUStat(content []byte) (cpuSample, error) {
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("cpu ")) {
			continue
		}
		fields := bytes.Fields(line)
		// Fields: cpu user nice system idle iowait irq softirq steal [...]
		if len(fields) < 9 {
			return cpuSample{}, fmt.Errorf("cpu line has %d fields, want ≥9", len(fields))
		}
		var v [8]uint64
		for i := range 8 {
			n, err := strconv.ParseUint(string(fields[i+1]), 10, 64)
			if err != nil {
				return cpuSample{}, fmt.Errorf("parse cpu field %d: %w", i+1, err)
			}
			v[i] = n
		}
		// v: [user nice system idle iowait irq softirq steal]
		idle_val := v[3] + v[4]                                        // idle + iowait
		total_val := v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
		return cpuSample{idle: idle_val, total: total_val}, nil
	}
	return cpuSample{}, fmt.Errorf("no 'cpu ' line in content")
}

// cpuUtilPct computes CPU utilization from two consecutive /proc/stat samples.
func cpuUtilPct(prev, cur cpuSample) float32 {
	delta_total := cur.total - prev.total
	if delta_total == 0 {
		return 0
	}
	delta_idle := cur.idle - prev.idle
	return float32(delta_total-delta_idle) / float32(delta_total) * 100
}

// parseMemInfo extracts MemTotal and MemAvailable (in kB) from /proc/meminfo content.
func parseMemInfo(content []byte) (total_kb, available_kb uint64, err error) {
	var found int
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("MemTotal:")) {
			total_kb, err = parseMemInfoKB(line)
			if err != nil {
				return 0, 0, fmt.Errorf("MemTotal: %w", err)
			}
			found++
		} else if bytes.HasPrefix(line, []byte("MemAvailable:")) {
			available_kb, err = parseMemInfoKB(line)
			if err != nil {
				return 0, 0, fmt.Errorf("MemAvailable: %w", err)
			}
			found++
		}
		if found == 2 {
			return total_kb, available_kb, nil
		}
	}
	return 0, 0, fmt.Errorf("missing MemTotal or MemAvailable")
}

func parseMemInfoKB(line []byte) (uint64, error) {
	fields := bytes.Fields(line)
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed line %q", line)
	}
	return strconv.ParseUint(string(fields[1]), 10, 64)
}

// memUtilPct computes memory utilization from parsed /proc/meminfo values.
func memUtilPct(total_kb, available_kb uint64) float32 {
	if total_kb == 0 {
		return 0
	}
	return float32(total_kb-available_kb) / float32(total_kb) * 100
}

// parsePSIPressure extracts avg60 from the "some" line of a /proc/pressure/* file.
// Returns 0 if the file format is unexpected (older kernel without PSI).
func parsePSIPressure(content []byte) float32 {
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("some ")) {
			continue
		}
		for _, field := range bytes.Fields(line) {
			if !bytes.HasPrefix(field, []byte("avg60=")) {
				continue
			}
			v, err := strconv.ParseFloat(string(field[len("avg60="):]), 32)
			if err != nil {
				return 0
			}
			return float32(v)
		}
	}
	return 0
}

// parseNetDev aggregates rx+tx bytes and errors across all non-loopback interfaces.
func parseNetDev(content []byte) (netSample, error) {
	var s netSample
	var found int
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		line = bytes.TrimSpace(line)
		colon_idx := bytes.IndexByte(line, ':')
		if colon_idx < 0 {
			continue
		}
		iface := bytes.TrimSpace(line[:colon_idx])
		if bytes.Equal(iface, []byte("lo")) {
			continue
		}
		fields := bytes.Fields(line[colon_idx+1:])
		// Fields: rx_bytes rx_pkts rx_errs rx_drop ... tx_bytes tx_pkts tx_errs ...
		if len(fields) < 16 {
			continue
		}
		rx_bytes, err1 := strconv.ParseUint(string(fields[0]), 10, 64)
		rx_errs, err2 := strconv.ParseUint(string(fields[2]), 10, 64)
		tx_bytes, err3 := strconv.ParseUint(string(fields[8]), 10, 64)
		tx_errs, err4 := strconv.ParseUint(string(fields[10]), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		s.bytes_total += rx_bytes + tx_bytes
		s.errors_total += rx_errs + tx_errs
		found++
	}
	if found == 0 {
		return netSample{}, fmt.Errorf("no non-loopback interfaces found")
	}
	return s, nil
}

// netUtilBps computes bytes per second from two consecutive net samples.
func netUtilBps(prev, cur netSample, elapsed_sec float64) uint64 {
	if elapsed_sec <= 0 || cur.bytes_total <= prev.bytes_total {
		return 0
	}
	return uint64(float64(cur.bytes_total-prev.bytes_total) / elapsed_sec)
}

// parseDiskStats finds the most active non-virtual block device's ms_io value.
func parseDiskStats(content []byte) (diskSample, error) {
	var best diskSample
	var found int
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		fields := bytes.Fields(line)
		// Format: major minor name [11 counters]. Need ≥14 fields.
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if bytes.HasPrefix(name, []byte("loop")) ||
			bytes.HasPrefix(name, []byte("ram")) ||
			bytes.HasPrefix(name, []byte("dm-")) {
			continue
		}
		ms_io, err := strconv.ParseUint(string(fields[12]), 10, 64)
		if err != nil {
			continue
		}
		best.ms_io = max(best.ms_io, ms_io)
		found++
	}
	if found == 0 {
		return diskSample{}, fmt.Errorf("no block devices found")
	}
	return best, nil
}

// diskUtilPct computes disk utilization from two consecutive diskstats samples.
func diskUtilPct(prev, cur diskSample, elapsed_ms int64) float32 {
	if elapsed_ms <= 0 || cur.ms_io <= prev.ms_io {
		return 0
	}
	pct := float32(cur.ms_io-prev.ms_io) / float32(elapsed_ms) * 100
	// Multi-queue devices can report >100%; cap at 100.
	return min(pct, 100)
}
