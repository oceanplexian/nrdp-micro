package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nrdp_micro/logger"
)

// SystemMetrics holds various system-level metrics
type SystemMetrics struct {
	OpenFiles      int
	MemStats       runtime.MemStats
	Goroutines     int
	TCPConnections int
	Timestamp      time.Time
}

// ByteSize represents byte sizes in human-readable format
type ByteSize float64

const (
	_           = iota
	KB ByteSize = 1 << (10 * iota)
	MB
	GB
)

func (b ByteSize) String() string {
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1fG", b/GB)
	case b >= MB:
		return fmt.Sprintf("%.1fM", b/MB)
	case b >= KB:
		return fmt.Sprintf("%.1fK", b/KB)
	}
	return fmt.Sprintf("%.0f", b)
}

// GetMetrics collects current system metrics
func GetMetrics() SystemMetrics {
	metrics := SystemMetrics{
		Timestamp: time.Now(),
	}

	// Count and inspect open file descriptors
	entries, err := os.ReadDir("/proc/self/fd")
	if err == nil {
		count := 0
		// Use strings.Builder for efficient string concatenation
		var openFilesLog strings.Builder
		openFilesLog.WriteString("Open file descriptors:\n")
		
		for _, entry := range entries {
			// Try to read the symlink target
			if target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name())); err == nil {
				count++
				// Append to builder
				openFilesLog.WriteString(entry.Name())
				openFilesLog.WriteString(" -> ")
				openFilesLog.WriteString(target)
				openFilesLog.WriteString("\n")
			}
		}
		metrics.OpenFiles = count
		
		// Log open files at debug level if any were found
		if count > 0 {
			logger.Logf(logger.LevelDebug, openFilesLog.String())
		}
	}

	runtime.ReadMemStats(&metrics.MemStats)
	metrics.Goroutines = runtime.NumGoroutine()

	// Read TCP connections count
	if data, err := os.ReadFile("/proc/net/tcp"); err == nil {
		metrics.TCPConnections = strings.Count(string(data), "\n") - 1
	}

	return metrics
}

// String returns a compact string representation of the metrics
func (m SystemMetrics) String() string {
	msg := logger.Message{
		Event: "metrics",
		Data: map[string]interface{}{
			"files":      m.OpenFiles,
			"conns":      m.TCPConnections,
			"goroutines": m.Goroutines,
			"mem_alloc":  ByteSize(m.MemStats.Alloc),
			"mem_sys":    ByteSize(m.MemStats.Sys),
			"heap_alloc": ByteSize(m.MemStats.HeapAlloc),
			"heap_objs":  m.MemStats.HeapObjects,
		},
	}
	return msg.String()
}

// DetailString returns a detailed string representation of the metrics
func (m SystemMetrics) DetailString() string {
	msg := logger.Message{
		Event: "metrics_detail",
		Data: map[string]interface{}{
			"resources": map[string]interface{}{
				"files":      m.OpenFiles,
				"conns":      m.TCPConnections,
				"goroutines": m.Goroutines,
			},
			"memory": map[string]interface{}{
				"alloc":      ByteSize(m.MemStats.Alloc),
				"sys":        ByteSize(m.MemStats.Sys),
				"heap_alloc": ByteSize(m.MemStats.HeapAlloc),
				"heap_sys":   ByteSize(m.MemStats.HeapSys),
			},
			"gc": map[string]interface{}{
				"num":       m.MemStats.NumGC,
				"cpu_pct":   fmt.Sprintf("%.1f%%", m.MemStats.GCCPUFraction*100),
				"next_heap": ByteSize(m.MemStats.NextGC),
				"heap_objs": m.MemStats.HeapObjects,
			},
		},
	}
	return msg.String()
}

// HasSignificantChanges checks if metrics have changed significantly from previous
func (m SystemMetrics) HasSignificantChanges(old SystemMetrics) bool {
	const (
		filesDiffThreshold      = 10      // 10 more/less files
		connsDiffThreshold      = 5       // 5 more/less connections
		goroutinesDiffThreshold = 10      // 10 more/less goroutines
		memDiffThreshold        = 100 * MB // 100MB memory change
	)

	return abs(m.OpenFiles-old.OpenFiles) > filesDiffThreshold ||
		abs(m.TCPConnections-old.TCPConnections) > connsDiffThreshold ||
		abs(m.Goroutines-old.Goroutines) > goroutinesDiffThreshold ||
		abs64(int64(m.MemStats.HeapAlloc)-int64(old.MemStats.HeapAlloc)) > int64(memDiffThreshold)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
} 