package utils

import (
	"runtime"
	"time"
)

var startTime = time.Now()

// GetHealth constructs and returns the current health metrics for the service.
func GetHealth() Health {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return Health{
		Timestamp: time.Now().Unix(),
		Uptime:    time.Since(startTime).Seconds(),
		Metrics: Metrics{
			Goroutines:      runtime.NumGoroutine(),
			MemoryAllocMB:   float64(memStats.Alloc) / 1024 / 1024,
			EventsReceived:  0, // Placeholder
			EventsProcessed: 0, // Placeholder
		},
	}
}
