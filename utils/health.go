package utils

import (
	"time"
)

var startTime = time.Now()

// GetHealth returns the current health status of the service.
func GetHealth() Health {
	// In a real-world scenario, you'd check database connections, etc.
	// For now, we'll just return a healthy status.
	uptime := time.Since(startTime).String()
	return Health{
		Status: "OK",
		Uptime: uptime,
	}
}
