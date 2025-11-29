package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/utils"
)

// ServiceReport for a single service, adapted for JSON output to frontend
type ServiceReport struct {
	ID            string      `json:"id"`
	ShortName     string      `json:"short_name"` // Will use ID as short_name fallback if not present
	Type          string      `json:"type"`       // Service type from service map group (e.g., "cs", "be")
	Domain        string      `json:"domain"`
	Port          string      `json:"port"`
	Status        string      `json:"status"` // "online", "offline", "unknown"
	Uptime        string      `json:"uptime"` // formatted string
	Version       VersionInfo `json:"version"`
	HealthMessage string      `json:"health_message"`
	CPU           string      `json:"cpu"`    // formatted string (e.g., "1.2%")
	Memory        string      `json:"memory"` // formatted string (e.g., "5.3%")
}

// VersionInfo matches the structure expected from /service endpoint
type VersionInfo struct {
	Str string `json:"str"`
	Obj struct {
		Major     string `json:"major"`
		Minor     string `json:"minor"`
		Patch     string `json:"patch"`
		Branch    string `json:"branch"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
		Arch      string `json:"arch"`
		BuildHash string `json:"build_hash"`
	} `json:"obj"`
}

// SystemMonitorHandler collects status for all configured services and returns as JSON
func SystemMonitorHandler(w http.ResponseWriter, r *http.Request) {
	configuredServices, err := config.LoadServiceMap()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load service map: %v", err), http.StatusInternalServerError)
		return
	}

	var reports []ServiceReport

	// Define service type order for consistent sorting
	typeOrder := map[string]int{
		"fe":  0,
		"be":  1,
		"cs":  2,
		"th":  3,
		"os":  4,
		"cli": 5,
	}

	// Get sorted group keys to ensure consistent order
	var groupKeys []string
	for group := range configuredServices.Services {
		groupKeys = append(groupKeys, group)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		orderI, okI := typeOrder[groupKeys[i]]
		orderJ, okJ := typeOrder[groupKeys[j]]
		if okI && okJ {
			return orderI < orderJ
		}
		if okI {
			return true
		}
		if okJ {
			return false
		}
		return groupKeys[i] < groupKeys[j]
	})

	// Iterate through sorted service groups
	for _, group := range groupKeys {
		servicesInGroup := configuredServices.Services[group]

		// Sort services within each group by ID for consistent ordering
		sort.Slice(servicesInGroup, func(i, j int) bool {
			return servicesInGroup[i].ID < servicesInGroup[j].ID
		})

		for _, serviceDef := range servicesInGroup {
			report := checkService(serviceDef, group) // Pass the service type (group)
			reports = append(reports, report)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reports); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode service reports: %v", err), http.StatusInternalServerError)
	}
}

// checkService dispatches to appropriate status checker based on service type.
func checkService(service config.ServiceEntry, serviceType string) ServiceReport {
	// Populate basic info, ShortName will be ID as no ShortName field in ServiceEntry
	baseReport := ServiceReport{
		ID:        service.ID,
		ShortName: service.ID, // Use ID as ShortName fallback
		Type:      serviceType,
		Domain:    service.Domain,
		Port:      service.Port,
	}

	switch serviceType {
	case "cli":
		return newUnknownServiceReport(baseReport, "CLI service status cannot be checked directly from event service.")
	case "os":
		if strings.Contains(strings.ToLower(service.ID), "ollama") {
			return newUnknownServiceReport(baseReport, "Ollama service status checking is simplified for event service.")
		}
		// For other OS services (like Redis), we attempt an HTTP check if a port is defined,
		// or mark as unknown if not. Full Redis PING logic from dex-cli is not replicated here.
		if service.Port != "" {
			return checkHTTPStatus(baseReport)
		}
		return newUnknownServiceReport(baseReport, "OS service status cannot be checked directly without specific handler.")
	default: // All other service types are assumed to be HTTP-based (fe, be, cs, th)
		return checkHTTPStatus(baseReport)
	}
}

// newUnknownServiceReport creates a default report for services we can't fully check
func newUnknownServiceReport(baseReport ServiceReport, message string) ServiceReport {
	baseReport.Status = "unknown"
	baseReport.HealthMessage = message
	baseReport.Uptime = "N/A"
	baseReport.CPU = "N/A"
	baseReport.Memory = "N/A"
	baseReport.Version = VersionInfo{} // Empty version info
	return baseReport
}

// checkHTTPStatus checks a service via its new, unified /service endpoint
func checkHTTPStatus(baseReport ServiceReport) ServiceReport {
	report := baseReport // Start with base info

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "localhost" // Adjust for client-side access
	}
	url := fmt.Sprintf("http://%s:%s/service", host, report.Port)

	jsonResponse, err := utils.FetchURL(url, 2*time.Second)
	if err != nil {
		report.Status = "offline"
		report.HealthMessage = fmt.Sprintf("Failed to connect: %v", err)
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	var serviceHealthReport struct {
		Version VersionInfo `json:"version"`
		Health  struct {
			Status  string `json:"status"`
			Uptime  string `json:"uptime"`
			Message string `json:"message"`
		} `json:"health"`
		Metrics struct {
			CPU struct {
				Avg float64 `json:"avg"`
			} `json:"cpu"`
			Memory struct {
				Avg float64 `json:"avg"`
			} `json:"memory"`
		} `json:"metrics"`
	}

	if err := json.Unmarshal([]byte(jsonResponse), &serviceHealthReport); err != nil {
		report.Status = "offline"
		report.HealthMessage = fmt.Sprintf("Failed to parse /service response: %v", err)
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	report.Status = strings.ToLower(serviceHealthReport.Health.Status) // "ok" or "bad" -> "online", "offline"
	if report.Status == "ok" {                                         // Normalize status to "online" if "ok"
		report.Status = "online"
	} else {
		report.Status = "offline"
	}
	report.Uptime = serviceHealthReport.Health.Uptime
	report.Version = serviceHealthReport.Version
	report.HealthMessage = serviceHealthReport.Health.Message

	if serviceHealthReport.Metrics.CPU.Avg > 0 {
		report.CPU = fmt.Sprintf("%.1f%%", serviceHealthReport.Metrics.CPU.Avg)
	} else {
		report.CPU = "N/A"
	}
	if serviceHealthReport.Metrics.Memory.Avg > 0 {
		report.Memory = fmt.Sprintf("%.1f%%", serviceHealthReport.Metrics.Memory.Avg)
	} else {
		report.Memory = "N/A"
	}

	return report
}
