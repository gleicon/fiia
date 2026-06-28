package telemetry

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func TestParseCPUStat(t *testing.T) {
	content := readFixture(t, "proc_stat")
	s, err := parseCPUStat(content)
	if err != nil {
		t.Fatalf("parseCPUStat: %v", err)
	}
	// user=23456 nice=1234 system=5678 idle=91234 iowait=2345 irq=678 softirq=90 steal=0
	want_idle := uint64(91234 + 2345) // idle + iowait
	want_total := uint64(23456 + 1234 + 5678 + 91234 + 2345 + 678 + 90 + 0)
	if s.idle != want_idle {
		t.Errorf("idle: got %d, want %d", s.idle, want_idle)
	}
	if s.total != want_total {
		t.Errorf("total: got %d, want %d", s.total, want_total)
	}
}

func TestCPUUtilPct(t *testing.T) {
	// First sample: idle=35457, total=57660
	prev := cpuSample{idle: 35457, total: 57660}
	// Second sample: add 5000 to total (2500 busy + 2500 idle)
	cur := cpuSample{idle: 35457 + 2500, total: 57660 + 5000}
	pct := cpuUtilPct(prev, cur)
	// delta_total=5000, delta_idle=2500 → util=50%
	if pct < 49.9 || pct > 50.1 {
		t.Errorf("cpuUtilPct: got %.2f, want ~50.0", pct)
	}
}

func TestCPUUtilPctZeroDelta(t *testing.T) {
	s := cpuSample{idle: 1000, total: 2000}
	if pct := cpuUtilPct(s, s); pct != 0 {
		t.Errorf("zero delta should return 0, got %f", pct)
	}
}

func TestParseMemInfo(t *testing.T) {
	content := readFixture(t, "proc_meminfo")
	total_kb, avail_kb, err := parseMemInfo(content)
	if err != nil {
		t.Fatalf("parseMemInfo: %v", err)
	}
	if total_kb != 16384000 {
		t.Errorf("total_kb: got %d, want 16384000", total_kb)
	}
	if avail_kb != 8192000 {
		t.Errorf("available_kb: got %d, want 8192000", avail_kb)
	}
}

func TestMemUtilPct(t *testing.T) {
	pct := memUtilPct(16384000, 8192000)
	// (16384000 - 8192000) / 16384000 * 100 = 50%
	if pct < 49.9 || pct > 50.1 {
		t.Errorf("memUtilPct: got %.2f, want ~50.0", pct)
	}
}

func TestParsePSIPressure(t *testing.T) {
	content := readFixture(t, "proc_pressure_cpu")
	avg60 := parsePSIPressure(content)
	if avg60 < 1.49 || avg60 > 1.51 {
		t.Errorf("parsePSIPressure avg60: got %.2f, want ~1.50", avg60)
	}
}

func TestParsePSIPressureEmpty(t *testing.T) {
	if v := parsePSIPressure([]byte("no psi data here")); v != 0 {
		t.Errorf("expected 0 for malformed input, got %f", v)
	}
}

func TestParseNetDev(t *testing.T) {
	content := readFixture(t, "proc_net_dev")
	s, err := parseNetDev(content)
	if err != nil {
		t.Fatalf("parseNetDev: %v", err)
	}
	// eth0: rx=1234567+tx=654321, eth1: rx=987654+tx=543210
	want_bytes := uint64(1234567 + 654321 + 987654 + 543210)
	if s.bytes_total != want_bytes {
		t.Errorf("bytes_total: got %d, want %d", s.bytes_total, want_bytes)
	}
	// eth0: rx_err=2+tx_err=1, eth1: rx_err=3+tx_err=2
	want_errs := uint64(2 + 1 + 3 + 2)
	if s.errors_total != want_errs {
		t.Errorf("errors_total: got %d, want %d", s.errors_total, want_errs)
	}
}

func TestNetUtilBps(t *testing.T) {
	prev := netSample{bytes_total: 1000}
	cur := netSample{bytes_total: 3000}
	// delta=2000 over 2 seconds → 1000 bps
	if bps := netUtilBps(prev, cur, 2.0); bps != 1000 {
		t.Errorf("netUtilBps: got %d, want 1000", bps)
	}
}

func TestParseDiskStats(t *testing.T) {
	content := readFixture(t, "proc_diskstats")
	s, err := parseDiskStats(content)
	if err != nil {
		t.Fatalf("parseDiskStats: %v", err)
	}
	// loop0 skipped; max ms_io among sda(12357), sda1(11000), nvme0n1(34567), nvme0n1p1(30000) = 34567
	if s.ms_io != 34567 {
		t.Errorf("ms_io: got %d, want 34567", s.ms_io)
	}
}

func TestDiskUtilPct(t *testing.T) {
	prev := diskSample{ms_io: 0}
	cur := diskSample{ms_io: 500}
	// 500ms io in 1000ms elapsed → 50%
	if pct := diskUtilPct(prev, cur, 1000); pct < 49.9 || pct > 50.1 {
		t.Errorf("diskUtilPct: got %.2f, want ~50.0", pct)
	}
}

func TestDiskUtilPctCap(t *testing.T) {
	prev := diskSample{ms_io: 0}
	cur := diskSample{ms_io: 5000}
	// 5000ms io in 1000ms → would be 500%, capped at 100
	if pct := diskUtilPct(prev, cur, 1000); pct != 100 {
		t.Errorf("diskUtilPct cap: got %.2f, want 100.0", pct)
	}
}
