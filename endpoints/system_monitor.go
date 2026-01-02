package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

var redisClient *redis.Client

// SetRedisClient sets the Redis client for endpoints
func SetRedisClient(client *redis.Client) {
	redisClient = client
}

// ProcessInfo represents the data stored by a handler in Redis for the processes tab
type ProcessInfo struct {
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
	Retries   int    `json:"retries"`
	StartTime int64  `json:"start_time"`
	PID       int    `json:"pid"`
	UpdatedAt int64  `json:"updated_at"`
	EndTime   int64  `json:"end_time,omitempty"`
}

// ProcessesSnapshot represents the full state of processes
type ProcessesSnapshot struct {
	Active  []ProcessInfo `json:"active"`
	Queue   []ProcessInfo `json:"queue"`
	History []ProcessInfo `json:"history"`
}

// GetProcessesSnapshot captures the current state of processes from Redis
func GetProcessesSnapshot() *ProcessesSnapshot {
	if redisClient == nil {
		return &ProcessesSnapshot{
			Active:  []ProcessInfo{},
			Queue:   []ProcessInfo{},
			History: []ProcessInfo{},
		}
	}

	ctx := context.Background()

	// 1. Fetch Active Processes
	activeProcesses := []ProcessInfo{}
	iter := redisClient.Scan(ctx, 0, "process:info:*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		val, err := redisClient.Get(ctx, key).Result()
		if err == nil {
			var pi ProcessInfo
			if err := json.Unmarshal([]byte(val), &pi); err == nil {
				activeProcesses = append(activeProcesses, pi)
			}
		}
	}

	// 2. Fetch Queue
	queuedProcesses := []ProcessInfo{}
	qIter := redisClient.Scan(ctx, 0, "process:queued:*", 0).Iterator()
	for qIter.Next(ctx) {
		key := qIter.Val()
		val, err := redisClient.Get(ctx, key).Result()
		if err == nil {
			var pi ProcessInfo
			if err := json.Unmarshal([]byte(val), &pi); err == nil {
				queuedProcesses = append(queuedProcesses, pi)
			}
		}
	}

	// 3. Fetch History
	historyProcesses := []ProcessInfo{}
	historyVals, err := redisClient.LRange(ctx, "process:history", 0, -1).Result()
	if err == nil {
		for _, val := range historyVals {
			var pi ProcessInfo
			if err := json.Unmarshal([]byte(val), &pi); err == nil {
				historyProcesses = append(historyProcesses, pi)
			}
		}
	}

	return &ProcessesSnapshot{
		Active:  activeProcesses,
		Queue:   queuedProcesses,
		History: historyProcesses,
	}
}

// ListProcessesHandler fetches and returns information about active event handler processes
func ListProcessesHandler(w http.ResponseWriter, r *http.Request) {
	if redisClient == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	snapshot := GetProcessesSnapshot()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
	}
}

// ModelReport for a single model's status
type ModelReport struct {
	Name   string `json:"name"`
	Type   string `json:"type"`   // "base" or "custom"
	Status string `json:"status"` // "Downloaded" or "Missing"
	Size   int64  `json:"size"`
}

// SystemMonitorResponse is the top-level response for the system monitor endpoint
type SystemMonitorResponse struct {
	Services []types.ServiceReport `json:"services"`
	Models   []ModelReport         `json:"models"`
	Whisper  *WhisperStatusReport  `json:"whisper,omitempty"`
	XTTS     *XTTSStatusReport     `json:"xtts,omitempty"`
}

// ... (omitting middle parts for now, will find exact locations)

// WhisperStatusReport provides status for the Whisper model environment
type WhisperStatusReport struct {
	Status string `json:"status"` // "Ready" or "Not Initialized"
	Path   string `json:"path"`
}

// XTTSStatusReport provides status for the XTTS model environment
type XTTSStatusReport struct {
	Status string `json:"status"` // "Ready" or "Not Initialized"
	Path   string `json:"path"`
}

// checkWhisperStatus checks if the Whisper model has been initialized
func checkWhisperStatus() *WhisperStatusReport {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil // Cannot determine home directory
	}
	modelDir := filepath.Join(home, "Dexter", "models", "whisper", "large-v3-turbo")

	report := &WhisperStatusReport{Path: modelDir}
	if _, err := os.Stat(modelDir); err == nil {
		report.Status = "Ready"
	} else {
		report.Status = "Not Initialized"
	}
	return report
}

// checkXTTSStatus checks if the XTTS model has been initialized
func checkXTTSStatus() *XTTSStatusReport {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	modelDir := filepath.Join(home, "Dexter", "models", "xtts")

	report := &XTTSStatusReport{Path: modelDir}
	// XTTS is ready if the directory exists and contains a config.json (or just check dir)
	if _, err := os.Stat(filepath.Join(modelDir, "config.json")); err == nil {
		report.Status = "Ready"
	} else {
		report.Status = "Not Initialized"
	}
	return report
}

// GetSystemMonitorSnapshot captures the full system state
func GetSystemMonitorSnapshot(isPublic bool) *SystemMonitorResponse {
	configuredServices, err := config.LoadServiceMap()
	if err != nil {
		// Return empty or error state if we can't load config
		return &SystemMonitorResponse{
			Services: []types.ServiceReport{},
			Models:   []ModelReport{},
		}
	}

	var serviceReports []types.ServiceReport

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

	// Iterate through sorted service groups to get service reports
	for _, group := range groupKeys {
		servicesInGroup := configuredServices.Services[group]
		sort.Slice(servicesInGroup, func(i, j int) bool {
			return servicesInGroup[i].ID < servicesInGroup[j].ID
		})
		for _, serviceDef := range servicesInGroup {
			report := checkService(serviceDef, group, isPublic)
			serviceReports = append(serviceReports, report)
		}
	}

	// Get model reports
	modelReports := checkModelsStatus()

	// Get whisper status report
	whisperReport := checkWhisperStatus()

	// Get XTTS status report
	xttsReport := checkXTTSStatus()

	return &SystemMonitorResponse{
		Services: serviceReports,
		Models:   modelReports,
		Whisper:  whisperReport,
		XTTS:     xttsReport,
	}
}

// DashboardSnapshot represents the full state of the dashboard for public consumption
type DashboardSnapshot struct {
	Monitor          *SystemMonitorResponse `json:"monitor"`
	Processes        *ProcessesSnapshot     `json:"processes"`
	Events           []types.Event          `json:"events"`
	MessagingEvents  []types.Event          `json:"messaging_events"`
	SystemEvents     []types.Event          `json:"system_events"`
	CognitiveEvents  []types.Event          `json:"cognitive_events"`
	ModerationEvents []types.Event          `json:"moderation_events"`
	Alerts           []types.Event          `json:"alerts"`
	Blueprints       []types.Event          `json:"blueprints"`
	Contacts         *ContactsResponse      `json:"contacts"`
	Profiles         map[string]interface{} `json:"profiles"`
	AgentStatus      map[string]interface{} `json:"agent_status"`
	Timestamp        int64                  `json:"timestamp"`
}

type MemberContext struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	Level     string `json:"level"`
	Color     int    `json:"color"`
	Status    string `json:"status"`
	Gender    string `json:"gender"`
}
type ContactsResponse struct {
	GuildName string          `json:"guild_name"`
	Members   []MemberContext `json:"members"`
}

// GetDashboardSnapshot captures the full system state for public mirroring
func GetDashboardSnapshot() *DashboardSnapshot {
	ctx := context.Background()

	// 1. Monitor State
	monitor := GetSystemMonitorSnapshot(true)

	// 2. Processes State
	processes := GetProcessesSnapshot()
	// Limit history in public snapshot to keep payload small
	if len(processes.History) > 10 {
		processes.History = processes.History[:10]
	}

	// 3. Sanitized Events (Global + Categorized)
	allEvents := getSanitizedEvents(ctx, "events:timeline", 250)

	messaging := []types.Event{}
	system := []types.Event{}
	cognitive := []types.Event{}
	moderation := []types.Event{}

	for _, e := range allEvents {
		var ed map[string]interface{}
		if err := json.Unmarshal(e.Event, &ed); err == nil {
			eventType, _ := ed["type"].(string)
			category := getCategoryFromType(eventType)

			switch category {
			case "messaging":
				if len(messaging) < 50 {
					messaging = append(messaging, e)
				}
			case "system":
				if len(system) < 50 {
					system = append(system, e)
				}
			case "cognitive":
				if len(cognitive) < 50 {
					cognitive = append(cognitive, e)
				}
			case "moderation":
				if len(moderation) < 50 {
					moderation = append(moderation, e)
				}
			}
		}
	}

	// 4. Sanitized Alerts
	alerts := getSanitizedEvents(ctx, "events:type:system.notification.generated", 50)

	// 4b. Sanitized Blueprints (Limit to 4 for public)
	blueprints := getSanitizedEvents(ctx, "events:type:system.blueprint.generated", 4)

	// 5. Public Contacts & Profiles (Owen & Dexter only)
	owenID := "313071000877137920"
	// Dexter's ID will be found via Level "Me"

	contacts := &ContactsResponse{
		GuildName: "Easter Company",
		Members:   []MemberContext{},
	}
	profiles := make(map[string]interface{})

	// 6. Agent Status
	agentStatus := GetAgentStatusSnapshot(redisClient)

	// Try to find Dexter's ID from the contacts cache if it exists
	if redisClient != nil {
		// 1. Scan for all contact cache keys
		iter := redisClient.Scan(ctx, 0, "cache:contacts:*", 0).Iterator()
		for iter.Next(ctx) {
			var cachedData struct {
				GuildName string          `json:"guild_name"`
				Members   []MemberContext `json:"members"`
			}
			data, _ := redisClient.Get(ctx, iter.Val()).Result()
			if err := json.Unmarshal([]byte(data), &cachedData); err == nil {
				if cachedData.GuildName != "" {
					contacts.GuildName = cachedData.GuildName
				}
				for _, m := range cachedData.Members {
					// Check if we already added this member to avoid duplicates
					isDuplicate := false
					for _, existing := range contacts.Members {
						if existing.ID == m.ID {
							isDuplicate = true
							break
						}
					}
					if isDuplicate {
						continue
					}

					// We include Owen (Master) and Dexter (Me)
					if m.ID == owenID || m.Level == "Me" {
						contacts.Members = append(contacts.Members, m)

						// Also fetch their full profile/dossier
						profileData, err := redisClient.Get(ctx, "user:profile:"+m.ID).Result()
						if err == nil {
							var p interface{}
							if err := json.Unmarshal([]byte(profileData), &p); err == nil {
								profiles[m.ID] = p
							}
						}
					}
				}
			}
		}

		// Fallback: If Owen was not in the contacts cache (offline), try to fetch his profile anyway
		if _, found := profiles[owenID]; !found {
			profileData, err := redisClient.Get(ctx, "user:profile:"+owenID).Result()
			if err == nil {
				var p interface{}
				if err := json.Unmarshal([]byte(profileData), &p); err == nil {
					profiles[owenID] = p
					// Also add a minimal MemberContext if missing
					hasOwenMember := false
					for _, m := range contacts.Members {
						if m.ID == owenID {
							hasOwenMember = true
							break
						}
					}
					if !hasOwenMember {
						contacts.Members = append(contacts.Members, MemberContext{
							ID:        owenID,
							Username:  "oweneaster",
							AvatarURL: "",
							Level:     "Master",
							Status:    "offline",
						})
					}
				}
			}
		}
	}

	return &DashboardSnapshot{
		Monitor:          monitor,
		Processes:        processes,
		Events:           allEvents,
		MessagingEvents:  messaging,
		SystemEvents:     system,
		CognitiveEvents:  cognitive,
		ModerationEvents: moderation,
		Alerts:           alerts,
		Blueprints:       blueprints,
		Contacts:         contacts,
		Profiles:         profiles,
		AgentStatus:      agentStatus,
		Timestamp:        time.Now().Unix(),
	}
}

func getSanitizedEvents(ctx context.Context, key string, count int) []types.Event {
	if redisClient == nil {
		return []types.Event{}
	}

	ids, err := redisClient.ZRevRange(ctx, key, 0, int64(count-1)).Result()
	if err != nil {
		return []types.Event{}
	}

	events := make([]types.Event, 0, len(ids))
	for _, id := range ids {
		val, err := redisClient.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}

		var event types.Event
		if err := json.Unmarshal([]byte(val), &event); err != nil {
			continue
		}

		// SANITIZATION: Strip heavy fields for public payload
		var eventData map[string]interface{}
		if err := json.Unmarshal(event.Event, &eventData); err == nil {
			// Remove fields not needed for display
			delete(eventData, "raw_input")
			delete(eventData, "raw_output")
			delete(eventData, "chat_history")
			delete(eventData, "context_history")
			delete(eventData, "engagement_raw")
			delete(eventData, "response_raw")

			// MASKING: Hide sensitive message content for public view
			// PRESERVE: Titles for Blueprints/Notifications are now allowed
			if _, ok := eventData["content"]; ok {
				eventData["content"] = "[Encrypted Content]"
			}

			// Re-marshal sanitized data
			cleanJSON, _ := json.Marshal(eventData)
			event.Event = cleanJSON
		}

		events = append(events, event)
	}

	return events
}

// SystemMonitorHandler collects status for all configured services and returns as JSON
func SystemMonitorHandler(w http.ResponseWriter, r *http.Request) {
	response := GetSystemMonitorSnapshot(false)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
	}
}

// SystemHardwareHandler returns the output of 'dex system' as JSON
func SystemHardwareHandler(w http.ResponseWriter, r *http.Request) {
	output, err := fetchSystemHardwareInfo(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch system info: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// The output from fetchSystemHardwareInfo is already JSON string (bytes), so we can just write it.
	// But we should validate it's valid JSON to be safe, or just pass it through.
	// Since fetchSystemHardwareInfo returns []byte or string, let's just write it.
	if _, err := w.Write(output); err != nil {
		fmt.Printf("Error writing response: %v\n", err)
	}
}

func fetchSystemHardwareInfo(ctx context.Context) ([]byte, error) {
	// Run 'dex system --json' to get hardware info
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dexPath := filepath.Join(home, "Dexter", "bin", "dex")

	cmd := exec.CommandContext(ctx, dexPath, "system", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to run dex system: %v (output: %s)", err, string(output))
	}
	return output, nil
}

// checkModelsStatus reports on all models currently downloaded in Ollama.
func checkModelsStatus() []ModelReport {
	downloadedModels, err := utils.ListOllamaModels()
	if err != nil {
		return []ModelReport{} // Ollama is likely offline
	}

	// Filter redundant ":latest" tags if a specific tag already exists for the SAME model.
	// e.g. if we have "gemma3:12b" and "gemma3:12b:latest", we only show "gemma3:12b".
	// But we MUST keep "gemma3:1b" and "gemma3:12b" as distinct entries.
	finalModels := make(map[string]utils.ModelInfo)

	for _, model := range downloadedModels {
		cleanName := strings.TrimSuffix(model.Name, ":latest")

		existing, exists := finalModels[cleanName]
		if !exists {
			finalModels[cleanName] = model
		} else {
			// If we have a conflict, prefer the one that DOESN'T have ":latest" in its raw name
			if strings.HasSuffix(existing.Name, ":latest") && !strings.HasSuffix(model.Name, ":latest") {
				finalModels[cleanName] = model
			}
		}
	}

	// Now, create the final reports from the filtered map.
	var reports []ModelReport
	for _, model := range finalModels {
		report := ModelReport{
			Name:   model.Name,
			Status: "Downloaded",
			Size:   model.Size,
		}
		if strings.HasPrefix(model.Name, "dex-") {
			report.Type = "custom"
		} else {
			report.Type = "base"
		}
		reports = append(reports, report)
	}

	// Sort reports by name for consistent order
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Name < reports[j].Name
	})

	return reports
}

// checkService dispatches to appropriate status checker based on service type.
func checkService(service config.ServiceEntry, serviceType string, isPublic bool) types.ServiceReport {
	// Populate basic info, ShortName will be ID as no ShortName field in ServiceEntry
	baseReport := types.ServiceReport{
		ID:        service.ID,
		ShortName: service.ID, // Use ID as ShortName fallback
		Type:      serviceType,
		Domain:    service.Domain,
		Port:      service.Port,
	}

	var report types.ServiceReport

	switch serviceType {
	case "cli":
		report = checkCLIStatus(baseReport)
	case "os":
		// Check for Ollama services
		if strings.Contains(strings.ToLower(service.ID), "ollama") {
			report = checkOllamaStatus(baseReport)
		} else if strings.Contains(strings.ToLower(service.ID), "cache") || strings.Contains(strings.ToLower(service.ID), "upstash") {
			// Check for Redis cache services
			report = checkRedisStatus(baseReport, service.Credentials, isPublic)
		} else if service.Port != "" {
			// For other OS services, attempt HTTP check if port is defined
			report = checkHTTPStatus(baseReport)
		} else {
			report = newUnknownServiceReport(baseReport, "OS service status cannot be checked directly without specific handler.")
		}
	default: // All other service types are assumed to be HTTP-based (fe, be, cs, th)
		report = checkHTTPStatus(baseReport)
	}

	// Post-check processing for Public Dashboard
	if isPublic {
		// 1. Obfuscate Address
		report.Domain = "easter.company"
		report.Port = ""

		// 2. Specialized Naming & Mocking for Cloud/System Services
		lowerID := strings.ToLower(report.ID)
		switch {
		case lowerID == "upstash-redis-ro":
			report.ShortName = "public-cache-1"
			report.Uptime = "∞"
		case lowerID == "upstash-redis-rw":
			report.ShortName = "public-cache-2"
			report.Uptime = "∞"
		case lowerID == "dex-cli":
			report.Uptime = "∞"
		case strings.Contains(lowerID, "cloud-cache"):
			report.Status = "online"
			report.HealthMessage = "System is operational"
			report.Version.Str = "Cloud"
			report.Uptime = "∞"
			report.CPU = "0.2%"
			report.Memory = "1.4%"
		case strings.Contains(lowerID, "upstash"):
			report.Uptime = "∞"
			report.CPU = "0.2%"
			report.Memory = "1.4%"
		}
	}

	return report
}

// newUnknownServiceReport creates a default report for services we can't fully check
func newUnknownServiceReport(baseReport types.ServiceReport, message string) types.ServiceReport {
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
func checkCLIStatus(baseReport types.ServiceReport) types.ServiceReport {
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
func checkHTTPStatus(baseReport types.ServiceReport) types.ServiceReport {
	report := baseReport // Start with base info

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "127.0.0.1" // Adjust for client-side access
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
			var avg float64
			found := true
			if val, ok := cpu["avg"].(float64); ok {
				avg = val
			} else if val, ok := cpu["avg"].(int64); ok {
				avg = float64(val)
			} else if val, ok := cpu["avg"].(int); ok {
				avg = float64(val)
			} else {
				found = false
			}

			if found {
				report.CPU = fmt.Sprintf("%.1f%%", avg)
			}
		}
	} else {
		report.CPU = "N/A"
	}

	if serviceHealthReport.Metrics["memory"] != nil {
		if mem, ok := serviceHealthReport.Metrics["memory"].(map[string]interface{}); ok {
			var avg float64
			found := true
			if val, ok := mem["avg"].(float64); ok {
				avg = val
			} else if val, ok := mem["avg"].(int64); ok {
				avg = float64(val)
			} else if val, ok := mem["avg"].(int); ok {
				avg = float64(val)
			} else {
				found = false
			}

			if found {
				// Most Dexter services report memory in MB
				report.Memory = fmt.Sprintf("%.1f MB", avg)
			}
		}
	} else {
		report.Memory = "N/A"
	}
	return report
}

// checkRedisStatus checks a Redis server via PING and INFO commands
func checkRedisStatus(baseReport types.ServiceReport, creds *config.ServiceCredentials, isPublic bool) types.ServiceReport {
	report := baseReport

	// MOCKED STATUS FOR UPSTASH IN PUBLIC SNAPSHOTS
	// If we are generating a public snapshot, we assume Upstash is Online because
	// we are currently using it to host the snapshot. This saves ops/latency.
	if isPublic && strings.Contains(strings.ToLower(report.ID), "upstash") {
		report.Status = "online"
		report.HealthMessage = "System is operational"
		report.Uptime = "∞"
		report.CPU = "0.2%"
		report.Memory = "1.4%"
		report.Version.Str = "Cloud"
		return report
	}

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%s", host, report.Port)

	var password string
	if creds != nil {
		password = creds.Password
	}

	// Create Redis client with timeout
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
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
func checkOllamaStatus(baseReport types.ServiceReport) types.ServiceReport {
	report := baseReport

	host := report.Domain
	if report.Domain == "0.0.0.0" {
		host = "127.0.0.1"
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

func getCategoryFromType(eventType string) string {
	categories := map[string][]string{
		"messaging": {
			"message_received", "message_sent", "messaging.user.sent_message",
			"messaging.bot.sent_message", "messaging.user.transcribed",
			"voice_transcribed", "messaging.user.joined_voice", "messaging.user.left_voice",
			"messaging.webhook.message",
		},
		"system": {
			"system.cli.command", "system.cli.status", "system.status.change",
			"metric_recorded", "log_entry", "error_occurred", "webhook.processed",
			"messaging.bot.status_update", "messaging.user.joined_server",
			"system.test.completed", "system.build.completed",
			"system.roadmap.created", "system.roadmap.updated",
			"system.process.registered", "system.process.unregistered",
		},
		"cognitive": {
			"engagement.decision", "system.analysis.audit", "system.blueprint.generated",
			"analysis.link.completed", "analysis.visual.completed", "analysis.router.decision",
			"analysis.user.message_signals",
		},
		"moderation": {
			"moderation.explicit_content.deleted",
		},
	}

	for cat, types := range categories {
		for _, t := range types {
			if t == eventType {
				return cat
			}
		}
	}
	return "system"
}
