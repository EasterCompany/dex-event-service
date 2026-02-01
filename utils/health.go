package utils

import (
	sharedUtils "github.com/EasterCompany/dex-go-utils/utils"
)

// GetHealth returns the current health status of the service.
func GetHealth() Health {
	return sharedUtils.GetHealth()
}

// GetUptimeSeconds returns the uptime in seconds.
func GetUptimeSeconds() int64 {
	return sharedUtils.GetUptimeSeconds()
}

// SetHealthStatus updates the health status of the service.
func SetHealthStatus(status string, message string) {
	sharedUtils.SetHealthStatus(status, message)
}
