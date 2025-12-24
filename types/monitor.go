package types

import "github.com/EasterCompany/dex-event-service/utils"

// ServiceReport for a single service, adapted for JSON output to frontend
type ServiceReport struct {
	ID            string        `json:"id"`
	ShortName     string        `json:"short_name"`
	Type          string        `json:"type"` // e.g., "cs", "be"
	Domain        string        `json:"domain"`
	Port          string        `json:"port"`
	Status        string        `json:"status"` // "online", "offline", "unknown"
	Uptime        string        `json:"uptime"`
	Version       utils.Version `json:"version"`
	HealthMessage string        `json:"health_message"`
	CPU           string        `json:"cpu"`
	Memory        string        `json:"memory"`
}

// LogReport for a single service
type LogReport struct {
	ID   string   `json:"id"`
	Logs []string `json:"logs"`
}
