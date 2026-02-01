package utils

import (
	"fmt"
	"sync"
	"time"
)

type HealthTracker struct {
	startTime     time.Time
	currentHealth Health
	mu            sync.RWMutex
}

var (
	defaultTracker *HealthTracker
	trackerOnce    sync.Once
)

func GetDefaultTracker() *HealthTracker {
	trackerOnce.Do(func() {
		defaultTracker = NewHealthTracker()
	})
	return defaultTracker
}

func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		startTime: time.Now(),
		currentHealth: Health{
			Status:  "STARTING",
			Uptime:  "0s",
			Message: "Service is initializing",
		},
	}
}

func (h *HealthTracker) GetHealth() Health {
	h.mu.RLock()
	defer h.mu.RUnlock()

	health := h.currentHealth
	health.Uptime = h.getFormattedUptime()
	return health
}

func (h *HealthTracker) GetUptimeSeconds() int64 {
	return int64(time.Since(h.startTime).Seconds())
}

func (h *HealthTracker) SetHealthStatus(status string, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.currentHealth.Status = status
	h.currentHealth.Message = message
	h.currentHealth.Uptime = h.getFormattedUptime()
}

func (h *HealthTracker) getFormattedUptime() string {
	duration := time.Since(h.startTime)
	days := int(duration.Hours() / 24)
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// Global helpers that use the default tracker
func GetHealth() Health {
	return GetDefaultTracker().GetHealth()
}

func SetHealthStatus(status string, message string) {
	GetDefaultTracker().SetHealthStatus(status, message)
}

func GetUptimeSeconds() int64 {
	return GetDefaultTracker().GetUptimeSeconds()
}
