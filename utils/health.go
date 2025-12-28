package utils

import (
	"sync"
	"time"
)

var (
	startTime     = time.Now()
	currentHealth = Health{
		Status:  "STARTING",
		Uptime:  "0s",
		Message: "Service is initializing",
	}
	healthMu sync.RWMutex
)

// GetHealth returns the current health status of the service.
// This function is thread-safe and can be called from multiple goroutines.
func GetHealth() Health {
	healthMu.RLock()
	defer healthMu.RUnlock()

	// Update uptime every time health is checked
	health := currentHealth
	health.Uptime = time.Since(startTime).String()
	return health
}

// GetUptimeSeconds returns the uptime in seconds.
func GetUptimeSeconds() int64 {
	return int64(time.Since(startTime).Seconds())
}

// SetHealthStatus updates the health status of the service.
// This function is thread-safe and can be called from multiple goroutines.
func SetHealthStatus(status string, message string) {
	healthMu.Lock()
	defer healthMu.Unlock()

	currentHealth.Status = status
	currentHealth.Message = message
	currentHealth.Uptime = time.Since(startTime).String()
}
