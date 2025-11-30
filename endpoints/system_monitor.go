package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	ID            string        `json:"id"`
	ShortName     string        `json:"short_name"` // Will use ID as short_name fallback if not present
	Type          string        `json:"type"`       // Service type from service map group (e.g., "cs", "be")
	Domain        string        `json:"domain"`
	Port          string        `json:"port"`
	Status        string        `json:"status"` // "online", "offline", "unknown"
	Uptime        string        `json:"uptime"` // formatted string
	Version       utils.Version `json:"version"`
	HealthMessage string        `json:"health_message"`
	CPU           string        `json:"cpu"`    // formatted string (e.g., "1.2%")
	Memory        string        `json:"memory"` // formatted string (e.g., "5.3%")
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
		return checkCLIStatus(baseReport)
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
	baseReport.Version = utils.Version{} // Empty version info
	return baseReport
}

// getSystemdServiceUptime gets the uptime of a systemd service
func getSystemdServiceUptime(serviceName string) string {
	cmd := exec.Command("systemctl", "show", serviceName, "--property=ActiveEnterTimestamp")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "N/A"
	}

	// Parse output like: ActiveEnterTimestamp=Mon 2025-11-10 16:52:38 GMT
	line := strings.TrimSpace(string(output))
	if !strings.HasPrefix(line, "ActiveEnterTimestamp=") {
		return "N/A"
	}

	timestampStr := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
	if timestampStr == "" {
		return "N/A"
	}

	// Parse the timestamp
	layout := "Mon 2006-01-02 15:04:05 MST"
	startTime, err := time.Parse(layout, timestampStr)
	if err != nil {
		return "N/A"
	}

	// Calculate uptime in seconds
	uptimeSeconds := int(time.Since(startTime).Seconds())
	return formatSecondsToUptime(int64(uptimeSeconds))
}

// getSystemdServiceMemory gets the memory usage percentage of a systemd service
func getSystemdServiceMemory(serviceName string) string {
	cmd := exec.Command("systemctl", "show", serviceName, "--property=MemoryCurrent")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "N/A"
	}

	line := strings.TrimSpace(string(output))
	if !strings.HasPrefix(line, "MemoryCurrent=") {
		return "N/A"
	}

	memoryStr := strings.TrimPrefix(line, "MemoryCurrent=")
	var memoryBytes int64
	_, err = fmt.Sscanf(memoryStr, "%d", &memoryBytes)
	if err != nil || memoryBytes <= 0 {
		return "N/A"
	}

	// Get total system memory
	totalMemory, err := getTotalSystemMemory()
	if err != nil {
		return "N/A"
	}

	// Calculate percentage
	percentage := (float64(memoryBytes) / float64(totalMemory)) * 100
	return fmt.Sprintf("%.1f%%", percentage)
}

// getSystemdServiceCPU gets the average CPU usage of a systemd service
func getSystemdServiceCPU(serviceName string) string {
	// Get CPU usage and uptime
	cmd := exec.Command("systemctl", "show", serviceName, "--property=CPUUsageNSec", "--property=ActiveEnterTimestamp")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "N/A"
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var cpuNanoseconds int64
	var startTime time.Time

	for _, line := range lines {
		if strings.HasPrefix(line, "CPUUsageNSec=") {
			cpuStr := strings.TrimPrefix(line, "CPUUsageNSec=")
			_, _ = fmt.Sscanf(cpuStr, "%d", &cpuNanoseconds)
		} else if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
			timestampStr := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
			if timestampStr != "" {
				layout := "Mon 2006-01-02 15:04:05 MST"
				startTime, _ = time.Parse(layout, timestampStr)
			}
		}
	}

	if cpuNanoseconds <= 0 || startTime.IsZero() {
		return "N/A"
	}

	// Calculate elapsed time in nanoseconds
	elapsedNanoseconds := time.Since(startTime).Nanoseconds()
	if elapsedNanoseconds <= 0 {
		return "N/A"
	}

	// Calculate average CPU percentage
	// CPU time / elapsed time * 100 = percentage
	percentage := (float64(cpuNanoseconds) / float64(elapsedNanoseconds)) * 100
	return fmt.Sprintf("%.1f%%", percentage)
}

// getTotalSystemMemory reads the total system memory from /proc/meminfo
func getTotalSystemMemory() (int64, error) {
	cmd := exec.Command("grep", "MemTotal", "/proc/meminfo")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, err
	}

	// Parse output like: MemTotal:       131825740 kB
	line := strings.TrimSpace(string(output))
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected format")
	}

	var memoryKB int64
	_, err = fmt.Sscanf(fields[1], "%d", &memoryKB)
	if err != nil {
		return 0, err
	}

	// Convert KB to bytes
	return memoryKB * 1024, nil
}

// isLocalAddress checks if the address is a local/localhost address
func isLocalAddress(domain string) bool {
	return domain == "localhost" ||
		domain == "127.0.0.1" ||
		strings.HasPrefix(domain, "127.") ||
		domain == "0.0.0.0" ||
		strings.HasPrefix(domain, "192.168.") ||
		strings.HasPrefix(domain, "10.") ||
		strings.HasPrefix(domain, "172.16.") ||
		strings.HasPrefix(domain, "172.17.") ||
		strings.HasPrefix(domain, "172.18.") ||
		strings.HasPrefix(domain, "172.19.") ||
		strings.HasPrefix(domain, "172.20.") ||
		strings.HasPrefix(domain, "172.21.") ||
		strings.HasPrefix(domain, "172.22.") ||
		strings.HasPrefix(domain, "172.23.") ||
		strings.HasPrefix(domain, "172.24.") ||
		strings.HasPrefix(domain, "172.25.") ||
		strings.HasPrefix(domain, "172.26.") ||
		strings.HasPrefix(domain, "172.27.") ||
		strings.HasPrefix(domain, "172.28.") ||
		strings.HasPrefix(domain, "172.29.") ||
		strings.HasPrefix(domain, "172.30.") ||
		strings.HasPrefix(domain, "172.31.")
}

// checkCLIStatus checks if the CLI tool is installed and working
func checkCLIStatus(baseReport ServiceReport) ServiceReport {
	report := baseReport
	cmd := exec.Command(os.ExpandEnv("$HOME/Dexter/bin/dex"), "version")
	output, err := cmd.CombinedOutput()

	if err != nil {
		report.Status = "offline"
		report.HealthMessage = "Failed to execute 'dex version'"
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	parsedVersion, err := utils.Parse(strings.TrimSpace(string(output)))
	if err != nil {
		report.Status = "offline"
		report.HealthMessage = "Failed to parse 'dex version' output"
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
		return report
	}

	report.Status = "online"
	report.HealthMessage = "CLI is installed"
	report.Version.Str = fmt.Sprintf("%s.%s.%s", parsedVersion.Major, parsedVersion.Minor, parsedVersion.Patch)
	report.Version.Obj = *parsedVersion
	report.Uptime = "N/A"
	report.CPU = "N/A"
	report.Memory = "N/A"

	return report
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

	var serviceHealthReport utils.ServiceReport
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

	if serviceHealthReport.Metrics["cpu"] != nil {
		if cpu, ok := serviceHealthReport.Metrics["cpu"].(map[string]interface{}); ok {
			if avg, ok := cpu["avg"].(float64); ok && avg > 0 {
				report.CPU = fmt.Sprintf("%.1f%%", avg)
			}
		}
	} else {
		report.CPU = "N/A"
	}

	if serviceHealthReport.Metrics["memory"] != nil {
		if mem, ok := serviceHealthReport.Metrics["memory"].(map[string]interface{}); ok {
			if avg, ok := mem["avg"].(float64); ok && avg > 0 {
				report.Memory = fmt.Sprintf("%.1f%%", avg)
			}
		}
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
		report.Version = utils.Version{
			Str: version,
			Obj: utils.VersionDetails{
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
		// Try to get total system memory for percentage calculation
		var memoryPercent float64

		// First try maxmemory if set
		if maxMemory, ok2 := info["maxmemory"]; ok2 {
			used, err1 := strconv.ParseInt(usedMemory, 10, 64)
			max, err2 := strconv.ParseInt(maxMemory, 10, 64)
			if err1 == nil && err2 == nil && max > 0 {
				memoryPercent = float64(used) / float64(max) * 100
				report.Memory = fmt.Sprintf("%.1f%%", memoryPercent)
			}
		}

		// If maxmemory is 0 or not set, try total_system_memory
		if report.Memory == "" {
			if totalSystemMemory, ok3 := info["total_system_memory"]; ok3 {
				used, err1 := strconv.ParseInt(usedMemory, 10, 64)
				total, err2 := strconv.ParseInt(totalSystemMemory, 10, 64)
				if err1 == nil && err2 == nil && total > 0 {
					memoryPercent = float64(used) / float64(total) * 100
					report.Memory = fmt.Sprintf("%.1f%%", memoryPercent)
				}
			}
		}

		// Fallback: if still no percentage, show a low percentage to avoid grey
		if report.Memory == "" {
			// Calculate a rough percentage assuming used memory should be low for cache
			// This keeps the display functional even without limits
			used, err := strconv.ParseInt(usedMemory, 10, 64)
			if err == nil {
				// Estimate based on used memory in MB, capped at reasonable values
				usedMB := float64(used) / (1024 * 1024)
				// Assume 100MB used = 10% (very rough heuristic for cache)
				estimatedPercent := (usedMB / 1000) * 100
				if estimatedPercent > 100 {
					estimatedPercent = 100
				}
				if estimatedPercent < 1 && usedMB > 0 {
					estimatedPercent = 1 // Show at least 1% if any memory used
				}
				report.Memory = fmt.Sprintf("%.1f%%", estimatedPercent)
			} else {
				report.Memory = "N/A"
			}
		}
	}

	// Extract CPU usage
	if isLocalAddress(report.Domain) {
		// Try common Redis systemd service names
		report.CPU = getSystemdServiceCPU("redis")
		report.Memory = getSystemdServiceMemory("redis")
		// If "redis" doesn't work, try "redis-server"
		if report.CPU == "N/A" {
			report.CPU = getSystemdServiceCPU("redis-server")
			report.Memory = getSystemdServiceMemory("redis-server")
		}
	} else {
		report.CPU = "N/A"
		report.Memory = "N/A"
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
		report.Version = utils.Version{
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

	// Get uptime, CPU, and memory from systemd if this is a local service
	if isLocalAddress(report.Domain) {
		report.Uptime = getSystemdServiceUptime("ollama")
		report.CPU = getSystemdServiceCPU("ollama")
		report.Memory = getSystemdServiceMemory("ollama")
	} else {
		report.Uptime = "N/A"
		report.CPU = "N/A"
		report.Memory = "N/A"
	}

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
