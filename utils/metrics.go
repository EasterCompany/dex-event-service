package utils

import (
	"runtime"
)

// SystemMetrics holds CPU and Memory usage statistics
type SystemMetrics struct {
	CPU    MetricValue `json:"cpu"`
	Memory MetricValue `json:"memory"`
}

// MetricValue represents a single metric with average value
type MetricValue struct {
	Avg float64 `json:"avg"`
}

// GetMetrics returns current CPU and Memory usage metrics
func GetMetrics() SystemMetrics {
	return SystemMetrics{
		CPU:    MetricValue{Avg: getCPUUsage()},
		Memory: MetricValue{Avg: getMemoryUsage()},
	}
}

// getCPUUsage calculates CPU usage percentage
func getCPUUsage() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// For Go runtime, we can estimate CPU usage based on GC and goroutines
	// This is a simplified approach - for accurate CPU usage, we'd need platform-specific code

	// Use number of goroutines as a proxy for activity
	numGoroutines := runtime.NumGoroutine()

	// Normalize to a percentage (assuming 100 goroutines = 50% usage as baseline)
	cpuPercent := float64(numGoroutines) / 2.0

	// Cap at 100%
	if cpuPercent > 100 {
		cpuPercent = 100
	}

	return cpuPercent
}

// getMemoryUsage returns memory usage in Megabytes (MB)
func getMemoryUsage() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// Return total system memory obtained from the OS in MB
	return float64(m.Sys) / 1024.0 / 1024.0
}
