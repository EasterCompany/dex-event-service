package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
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
		// Check for Ollama services
		if strings.Contains(strings.ToLower(service.ID), "ollama") {
			return checkOllamaStatus(baseReport)
		}
		// Check for Redis cache services
		if strings.Contains(strings.ToLower(service.ID), "cache") {
			return checkRedisStatus(baseReport)
		}
		// For other OS services, attempt HTTP check if port is defined
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

// checkRedisStatus checks a Redis server via PING and INFO commands
func checkRedisStatus(baseReport ServiceReport) ServiceReport {
	report := baseReport

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "localhost"
	}
	addr := fmt.Sprintf("%s:%s", host, report.Port)

	// Create Redis client with timeout
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})
	defer func() {
		_ = rdb.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to PING
	pong, err := rdb.Ping(ctx).Result()
	if err != nil {
		report.Status = "offline"
		report.HealthMessage = fmt.Sprintf("Failed to connect: %v", err)
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	if pong != "PONG" {
		report.Status = "offline"
		report.HealthMessage = "Unexpected PING response"
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	// Get server info
	infoStr, err := rdb.Info(ctx, "server", "memory", "cpu").Result()
	if err != nil {
		// Online but couldn't get info
		report.Status = "online"
		report.HealthMessage = "Connected but couldn't retrieve server info"
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	// Parse INFO response
	info := parseRedisInfo(infoStr)

	report.Status = "online"
	report.HealthMessage = "Redis server is responding"

	// Extract version
	if version, ok := info["redis_version"]; ok {
		report.Version = VersionInfo{
			Str: version,
			Obj: struct {
				Major     string `json:"major"`
				Minor     string `json:"minor"`
				Patch     string `json:"patch"`
				Branch    string `json:"branch"`
				Commit    string `json:"commit"`
				BuildDate string `json:"build_date"`
				Arch      string `json:"arch"`
				BuildHash string `json:"build_hash"`
			}{
				Major: strings.Split(version, ".")[0],
			},
		}
		if parts := strings.Split(version, "."); len(parts) >= 2 {
			report.Version.Obj.Minor = parts[1]
		}
		if parts := strings.Split(version, "."); len(parts) >= 3 {
			report.Version.Obj.Patch = parts[2]
		}
	}

	// Extract uptime
	if uptimeSeconds, ok := info["uptime_in_seconds"]; ok {
		if seconds, err := strconv.ParseInt(uptimeSeconds, 10, 64); err == nil {
			report.Uptime = formatSecondsToUptime(seconds)
		}
	}

	// Extract memory usage
	if usedMemory, ok := info["used_memory"]; ok {
		if maxMemory, ok2 := info["maxmemory"]; ok2 {
			used, err1 := strconv.ParseInt(usedMemory, 10, 64)
			max, err2 := strconv.ParseInt(maxMemory, 10, 64)
			if err1 == nil && err2 == nil && max > 0 {
				percentage := float64(used) / float64(max) * 100
				report.Memory = fmt.Sprintf("%.1f%%", percentage)
			} else {
				report.Memory = formatBytes(usedMemory)
			}
		} else {
			report.Memory = formatBytes(usedMemory)
		}
	}

	// Extract CPU usage (if available)
	if cpuSys, ok := info["used_cpu_sys"]; ok {
		if cpu, err := strconv.ParseFloat(cpuSys, 64); err == nil {
			report.CPU = fmt.Sprintf("%.1f%%", cpu)
		}
	} else {
		report.CPU = "N/A"
	}

	return report
}

// checkOllamaStatus checks an Ollama server via its API
func checkOllamaStatus(baseReport ServiceReport) ServiceReport {
	report := baseReport

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "localhost"
	}

	// Check version endpoint
	versionURL := fmt.Sprintf("http://%s:%s/api/version", host, report.Port)
	versionResponse, err := utils.FetchURL(versionURL, 2*time.Second)
	if err != nil {
		report.Status = "offline"
		report.HealthMessage = fmt.Sprintf("Failed to connect: %v", err)
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	var versionData struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(versionResponse), &versionData); err == nil {
		report.Version = VersionInfo{
			Str: versionData.Version,
		}
	}

	// Check tags endpoint to verify server is functional
	tagsURL := fmt.Sprintf("http://%s:%s/api/tags", host, report.Port)
	_, err = utils.FetchURL(tagsURL, 2*time.Second)
	if err != nil {
		report.Status = "offline"
		report.HealthMessage = "Service responded but is not functional"
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	report.Status = "online"
	report.HealthMessage = "Ollama server is running"
	report.Uptime = "N/A" // Ollama doesn't provide uptime
	report.CPU = "N/A"
	report.Memory = "N/A"

	return report
}

// parseRedisInfo parses the Redis INFO response into a key-value map
func parseRedisInfo(info string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(info, "\r\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// formatSecondsToUptime converts seconds to a readable uptime string
func formatSecondsToUptime(seconds int64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60

	if days > 0 {
		return fmt.Sprintf("%dd%dh%dm%ds", days, hours, minutes, secs)
	} else if hours > 0 {
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, secs)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatBytes formats a byte count string into a human-readable format
func formatBytes(bytesStr string) string {
	bytes, err := strconv.ParseInt(bytesStr, 10, 64)
	if err != nil {
		return bytesStr
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	units := []string{"KB", "MB", "GB", "TB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}

	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}
