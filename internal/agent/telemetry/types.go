package telemetry

// cpuSample holds cumulative CPU jiffies from /proc/stat.
type cpuSample struct {
	idle  uint64
	total uint64
}

// netSample holds cumulative network bytes and errors from /proc/net/dev.
type netSample struct {
	bytes_total  uint64
	errors_total uint64
}

// diskSample holds cumulative disk I/O milliseconds from /proc/diskstats.
type diskSample struct {
	ms_io uint64
}
