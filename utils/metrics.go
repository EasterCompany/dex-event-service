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

// getMemoryUsage calculates memory usage percentage
func getMemoryUsage() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Get allocated memory in bytes
	allocMB := float64(m.Alloc) / 1024 / 1024

	// Estimate total available memory (this is simplified)
	// For production, you'd want to read from /proc/meminfo on Linux
	// For now, use system memory if available, otherwise estimate based on allocations
	totalMB := float64(m.Sys) / 1024 / 1024

	if totalMB == 0 {
		return 0
	}

	memPercent := (allocMB / totalMB) * 100

	// Cap at 100%
	if memPercent > 100 {
		memPercent = 100
	}

	return memPercent
}
