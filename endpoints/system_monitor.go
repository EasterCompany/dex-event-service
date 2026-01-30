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
	"github.com/EasterCompany/dex-event-service/internal/chores"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

// SetRedisClient sets the Redis client for endpoints
func SetRedisClient(client *redis.Client) {
	RDB = client
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
	if RDB == nil {
		return &ProcessesSnapshot{
			Active:  []ProcessInfo{},
			Queue:   []ProcessInfo{},
			History: []ProcessInfo{},
		}
	}

	ctx := context.Background()

	// 1. Fetch Active Processes
	activeProcesses := []ProcessInfo{}
	iter := RDB.Scan(ctx, 0, "process:info:*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		val, err := RDB.Get(ctx, key).Result()
		if err == nil {
			var pi ProcessInfo
			if err := json.Unmarshal([]byte(val), &pi); err == nil {
				activeProcesses = append(activeProcesses, pi)
			}
		}
	}

	// 2. Fetch Queue
	queuedProcesses := []ProcessInfo{}
	qIter := RDB.Scan(ctx, 0, "process:queued:*", 0).Iterator()
	for qIter.Next(ctx) {
		key := qIter.Val()
		val, err := RDB.Get(ctx, key).Result()
		if err == nil {
			var pi ProcessInfo
			if err := json.Unmarshal([]byte(val), &pi); err == nil {
				queuedProcesses = append(queuedProcesses, pi)
			}
		}
	}

	// 3. Fetch History
	historyProcesses := []ProcessInfo{}
	historyVals, err := RDB.LRange(ctx, "process:history", 0, -1).Result()
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
	if RDB == nil {
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
	Services  []types.ServiceReport `json:"services"`
	NeuralSTT *STTStatusReport      `json:"neural_stt,omitempty"`
	NeuralTTS *TTSStatusReport      `json:"neural_tts,omitempty"`
}

// STTStatusReport provides status for the Neural STT environment
type STTStatusReport struct {
	Status string `json:"status"` // "Ready" or "Not Initialized"
	Path   string `json:"path"`
}

// TTSStatusReport provides status for the Neural TTS environment
type TTSStatusReport struct {
	Status string `json:"status"` // "Ready" or "Not Initialized"
	Path   string `json:"path"`
}

// checkSTTStatus checks if the STT model has been initialized
func checkSTTStatus() *STTStatusReport {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	modelFile := filepath.Join(home, "Dexter", "models", "dex-net-stt.bin")

	report := &STTStatusReport{Path: modelFile}
	if _, err := os.Stat(modelFile); err == nil {
		report.Status = "Ready"
	} else {
		report.Status = "Not Initialized"
	}
	return report
}

// checkTTSStatus checks if the TTS model has been initialized
func checkTTSStatus() *TTSStatusReport {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	modelFile := filepath.Join(home, "Dexter", "models", "dex-net-tts.onnx")

	report := &TTSStatusReport{Path: modelFile}
	if _, err := os.Stat(modelFile); err == nil {
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
		}
	}

	var serviceReports []types.ServiceReport

	// Define service type order for consistent sorting
	typeOrder := map[string]int{
		"cli": 0, "fe": 1, "cs": 2, "be": 3, "th": 4, "co": 5, "md": 6, "prd": 7, "os": 8,
	}

	// Get sorted group keys
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

	for _, group := range groupKeys {
		servicesInGroup := configuredServices.Services[group]
		sort.Slice(servicesInGroup, func(i, j int) bool {
			return servicesInGroup[i].ID < servicesInGroup[j].ID
		})
		for _, serviceDef := range servicesInGroup {
			id := strings.ToLower(serviceDef.ID)
			if id == "dex-cli" || id == "local-cache-0" || id == "easter-company-root" {
				continue
			}
			report := checkService(serviceDef, group, isPublic)
			serviceReports = append(serviceReports, report)
		}
	}

	modelReports := checkModelsAsServiceReports()
	serviceReports = append(serviceReports, modelReports...)
	sttReport := checkSTTStatus()
	ttsReport := checkTTSStatus()

	return &SystemMonitorResponse{
		Services: serviceReports, NeuralSTT: sttReport, NeuralTTS: ttsReport,
	}
}

// DashboardSnapshot represents the full state of the dashboard for public consumption
type DashboardSnapshot struct {
	Monitor              *SystemMonitorResponse `json:"monitor"`
	Processes            *ProcessesSnapshot     `json:"processes"`
	Events               []types.Event          `json:"events"`
	MessagingEvents      []types.Event          `json:"messaging_events"`
	SystemEvents         []types.Event          `json:"system_events"`
	CognitiveEvents      []types.Event          `json:"cognitive_events"`
	ModerationEvents     []types.Event          `json:"moderation_events"`
	Alerts               []types.Event          `json:"alerts"`
	GitHubIssues         []utils.GitHubIssue    `json:"github_issues"`
	OpenIssueCount       int                    `json:"open_issue_count"`
	Contacts             *ContactsResponse      `json:"contacts"`
	Profiles             map[string]interface{} `json:"profiles"`
	AgentStatus          map[string]interface{} `json:"agent_status"`
	Options              map[string]interface{} `json:"options"`
	WebView              map[string]interface{} `json:"web_view"`
	FabricatorLiveBuffer []string               `json:"fabricator_live_buffer"`
	Chores               []*chores.Chore        `json:"chores"`
	Timestamp            int64                  `json:"timestamp"`
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

	var filteredServices []types.ServiceReport
	for _, service := range monitor.Services {
		id := strings.ToLower(service.ID)
		if service.Type == "prd" {
			filteredServices = append(filteredServices, service)
			continue
		}
		if id == "upstash-redis-rw" || id == "easter-company" {
			continue
		}
		filteredServices = append(filteredServices, service)
	}
	monitor.Services = filteredServices

	// 2. Processes State
	processes := GetProcessesSnapshot()
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

	// 4b. GitHub Issues for Roadmap
	githubIssues, _ := utils.ListGitHubIssues()
	openIssueCount := len(githubIssues)

	// 5. Public Contacts & Profiles (Owen & Dexter only)
	owenID := "313071000877137920"
	contacts := &ContactsResponse{
		GuildName: "Easter Company",
		Members:   []MemberContext{},
	}
	profiles := make(map[string]interface{})

	// 6. Agent Status
	agentStatus := GetAgentStatusSnapshot(RDB)

	// 6b. System Options
	options := GetSystemOptionsSnapshot()

	// 7. Web View State (Sanitized)
	webView := make(map[string]interface{})
	if RDB != nil {
		val, err := RDB.Get(ctx, "state:web:view").Result()
		if err == nil {
			if err := json.Unmarshal([]byte(val), &webView); err == nil {
				// SANITIZATION: Remove screenshot data for public view
				if visual, ok := webView["visual"].(map[string]interface{}); ok {
					delete(visual, "screenshot")
					delete(visual, "screenshot_base64")
					delete(visual, "base64")
				}
			}
		}
	}

	// 8. Chores
	allChores := []*chores.Chore{}
	if RDB != nil {
		choreStore := chores.NewStore(RDB)
		if list, err := choreStore.GetAll(ctx); err == nil {
			allChores = list
		}
	}

	// 9. Fabricator Live Buffer
	fabBuffer := []string{}
	if RDB != nil {
		if val, err := RDB.LRange(ctx, "system:fabricator:live:buffer", -50, -1).Result(); err == nil {
			fabBuffer = val
		}
	}

	// Contacts & Profiles Logic
	if RDB != nil {
		iter := RDB.Scan(ctx, 0, "cache:contacts:*", 0).Iterator()
		for iter.Next(ctx) {
			var cachedData struct {
				GuildName string          `json:"guild_name"`
				Members   []MemberContext `json:"members"`
			}
			data, _ := RDB.Get(ctx, iter.Val()).Result()
			if err := json.Unmarshal([]byte(data), &cachedData); err == nil {
				if cachedData.GuildName != "" {
					contacts.GuildName = cachedData.GuildName
				}
				for _, m := range cachedData.Members {
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
					contacts.Members = append(contacts.Members, m)
					if m.ID == owenID || m.Level == "Me" {
						profileData, err := RDB.Get(ctx, "user:profile:"+m.ID).Result()
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

		if _, found := profiles[owenID]; !found {
			profileData, err := RDB.Get(ctx, "user:profile:"+owenID).Result()
			if err == nil {
				var p interface{}
				if err := json.Unmarshal([]byte(profileData), &p); err == nil {
					profiles[owenID] = p
					hasOwenMember := false
					for _, m := range contacts.Members {
						if m.ID == owenID {
							hasOwenMember = true
							break
						}
					}
					if !hasOwenMember {
						contacts.Members = append(contacts.Members, MemberContext{ID: owenID, Username: "oweneaster", Status: "offline", Level: "Master"})
					}
				}
			}
		}

		hasDexter := false
		for _, m := range contacts.Members {
			if m.Level == "Me" {
				hasDexter = true
				break
			}
		}
		if !hasDexter {
			pIter := RDB.Scan(ctx, 0, "user:profile:*", 0).Iterator()
			for pIter.Next(ctx) {
				pKey := pIter.Val()
				pData, _ := RDB.Get(ctx, pKey).Result()
				var profile map[string]interface{}
				if err := json.Unmarshal([]byte(pData), &profile); err == nil {
					identity, _ := profile["identity"].(map[string]interface{})
					badges, _ := identity["badges"].([]interface{})
					isBot := false
					for _, b := range badges {
						if b == "Me" || b == "Dexter" {
							isBot = true
							break
						}
					}
					if isBot {
						dID := strings.TrimPrefix(pKey, "user:profile:")
						username, _ := identity["username"].(string)
						avatar, _ := identity["avatar_url"].(string)
						contacts.Members = append(contacts.Members, MemberContext{ID: dID, Username: username, AvatarURL: avatar, Level: "Me", Status: "online"})
						profiles[dID] = profile
						break
					}
				}
			}
		}
	}

	return &DashboardSnapshot{
		Monitor: monitor, Processes: processes, Events: allEvents, MessagingEvents: messaging,
		SystemEvents: system, CognitiveEvents: cognitive, ModerationEvents: moderation, Alerts: alerts,
		GitHubIssues: githubIssues, OpenIssueCount: openIssueCount, Contacts: contacts, Profiles: profiles,
		AgentStatus: agentStatus, Options: options, WebView: webView, FabricatorLiveBuffer: fabBuffer, Chores: allChores, Timestamp: time.Now().Unix(),
	}
}

func getSanitizedEvents(ctx context.Context, key string, count int) []types.Event {
	if RDB == nil {
		return []types.Event{}
	}
	ids, err := RDB.ZRevRange(ctx, key, 0, int64(count-1)).Result()
	if err != nil {
		return []types.Event{}
	}
	events := make([]types.Event, 0, len(ids))
	for _, id := range ids {
		val, err := RDB.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}
		var event types.Event
		if err := json.Unmarshal([]byte(val), &event); err == nil {
			var eventData map[string]interface{}
			if err := json.Unmarshal(event.Event, &eventData); err == nil {
				eventType, _ := eventData["type"].(string)
				if eventType != "engagement.decision" {
					delete(eventData, "raw_input")
					delete(eventData, "raw_output")
					delete(eventData, "chat_history")
					delete(eventData, "context_history")
					delete(eventData, "engagement_raw")
					delete(eventData, "response_raw")
					delete(eventData, "base64_preview")
					delete(eventData, "input_prompt")

					// Remove top-level URL for non-link analysis events (Visual analysis, etc)
					if eventType != "analysis.link.completed" && eventType != "analysis.router.decision" {
						delete(eventData, "url")
					}
				}
				if attachments, ok := eventData["attachments"].([]interface{}); ok {
					for _, att := range attachments {
						if attMap, ok := att.(map[string]interface{}); ok {
							delete(attMap, "base64")
							delete(attMap, "url")
							delete(attMap, "proxy_url")
						}
					}
				}
				if _, ok := eventData["content"]; ok {
					eventData["content"] = "[Encrypted Content]"
				}
				cleanJSON, _ := json.Marshal(eventData)
				event.Event = cleanJSON
			}
			events = append(events, event)
		}
	}
	return events
}

func SystemMonitorHandler(w http.ResponseWriter, r *http.Request) {
	response := GetSystemMonitorSnapshot(false)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func SystemHardwareHandler(w http.ResponseWriter, r *http.Request) {
	output, err := fetchSystemHardwareInfo(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(output)
}

func fetchSystemHardwareInfo(ctx context.Context) ([]byte, error) {
	home, _ := os.UserHomeDir()
	dexPath := filepath.Join(home, "Dexter", "bin", "dex")
	return exec.CommandContext(ctx, dexPath, "system", "--json").CombinedOutput()
}

func checkModelsAsServiceReports() []types.ServiceReport {
	downloadedModels, err := utils.ListHubModels()
	if err != nil {
		return []types.ServiceReport{}
	}
	finalModels := make(map[string]utils.ModelInfo)
	for _, model := range downloadedModels {
		cleanName := strings.TrimSuffix(model.Name, ":latest")
		existing, exists := finalModels[cleanName]
		if !exists || (strings.HasSuffix(existing.Name, ":latest") && !strings.HasSuffix(model.Name, ":latest")) {
			finalModels[cleanName] = model
		}
	}
	var reports []types.ServiceReport
	for _, model := range finalModels {
		report := types.ServiceReport{
			ID: model.Name, ShortName: strings.TrimSuffix(model.Name, ":latest"), Type: "md", Status: "online",
			Memory: fmt.Sprintf("%.2f GB", float64(model.Size)/1e9), Uptime: "∞",
		}
		if strings.HasPrefix(model.Name, "dex-") {
			report.HealthMessage = "Custom Dexter Model"
		} else {
			report.HealthMessage = "Base Neural Model"
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].ID < reports[j].ID })
	return reports
}

func checkService(service config.ServiceEntry, serviceType string, isPublic bool) types.ServiceReport {
	baseReport := types.ServiceReport{
		ID: service.ID, ShortName: service.ID, Type: serviceType, Domain: service.Domain, Port: service.Port,
	}
	var report types.ServiceReport
	switch serviceType {
	case "cli":
		report = checkCLIStatus(baseReport)
	case "prd":
		report = checkProdStatus(baseReport)
	case "os":
		if strings.Contains(strings.ToLower(service.ID), "cache") || strings.Contains(strings.ToLower(service.ID), "upstash") {
			report = checkRedisStatus(baseReport, service.Credentials, isPublic)
		} else if service.Port != "" {
			report = checkHTTPStatus(baseReport)
		} else {
			report = newUnknownServiceReport(baseReport, "OS service status unknown")
		}
	default:
		report = checkHTTPStatus(baseReport)
	}
	if isPublic {
		if serviceType != "prd" {
			report.Domain = "easter.company"
			report.Port = ""
		}
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
		case lowerID == "easter-company-production":
			report.ShortName = "easter.company"
			report.Uptime = "∞"
		case strings.Contains(lowerID, "upstash"):
			report.Uptime = "∞"
			report.CPU = "0.2%"
			report.Memory = "1.4%"
		}
	}
	return report
}

func newUnknownServiceReport(baseReport types.ServiceReport, message string) types.ServiceReport {
	baseReport.Status = "unknown"
	baseReport.HealthMessage = message
	baseReport.Uptime = "N/A"
	baseReport.CPU = "N/A"
	baseReport.Memory = "N/A"
	return baseReport
}

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
	var memoryBytes int64
	_, _ = fmt.Sscanf(strings.TrimPrefix(line, "MemoryCurrent="), "%d", &memoryBytes)
	if memoryBytes <= 0 {
		return "N/A"
	}
	totalMemory, err := getTotalSystemMemory()
	if err != nil {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%%", (float64(memoryBytes)/float64(totalMemory))*100)
}

func getSystemdServiceCPU(serviceName string) string {
	cmd := exec.Command("systemctl", "show", serviceName, "--property=CPUUsageNSec", "--property=ActiveEnterTimestamp")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "N/A"
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var cpuNS int64
	var startTime time.Time
	for _, line := range lines {
		if strings.HasPrefix(line, "CPUUsageNSec=") {
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "CPUUsageNSec="), "%d", &cpuNS)
		} else if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
			tStr := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
			if tStr != "" {
				startTime, _ = time.Parse("Mon 2006-01-02 15:04:05 MST", tStr)
			}
		}
	}
	if cpuNS <= 0 || startTime.IsZero() {
		return "N/A"
	}
	elapsed := time.Since(startTime).Nanoseconds()
	if elapsed <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%%", (float64(cpuNS)/float64(elapsed))*100)
}

func getTotalSystemMemory() (int64, error) {
	output, err := exec.Command("grep", "MemTotal", "/proc/meminfo").CombinedOutput()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 0, fmt.Errorf("bad format")
	}
	var memoryKB int64
	_, _ = fmt.Sscanf(fields[1], "%d", &memoryKB)
	return memoryKB * 1024, nil
}

func isLocalAddress(domain string) bool {
	return domain == "localhost" || domain == "127.0.0.1" || strings.HasPrefix(domain, "127.") || domain == "0.0.0.0" ||
		strings.HasPrefix(domain, "192.168.") || strings.HasPrefix(domain, "10.") || strings.HasPrefix(domain, "172.16.") ||
		strings.HasPrefix(domain, "172.17.") || strings.HasPrefix(domain, "172.18.") || strings.HasPrefix(domain, "172.19.") ||
		strings.HasPrefix(domain, "172.20.") || strings.HasPrefix(domain, "172.21.") || strings.HasPrefix(domain, "172.22.") ||
		strings.HasPrefix(domain, "172.23.") || strings.HasPrefix(domain, "172.24.") || strings.HasPrefix(domain, "172.25.") ||
		strings.HasPrefix(domain, "172.26.") || strings.HasPrefix(domain, "172.27.") || strings.HasPrefix(domain, "172.28.") ||
		strings.HasPrefix(domain, "172.29.") || strings.HasPrefix(domain, "172.30.") || strings.HasPrefix(domain, "172.31.")
}

func checkCLIStatus(baseReport types.ServiceReport) types.ServiceReport {
	report := baseReport
	output, err := exec.Command(os.ExpandEnv("$HOME/Dexter/bin/dex"), "version").CombinedOutput()
	if err != nil {
		report.Status = "offline"
		return report
	}
	parsed, err := utils.Parse(strings.TrimSpace(string(output)))
	if err != nil {
		report.Status = "offline"
		return report
	}
	report.Status = "online"
	report.Version.Str = fmt.Sprintf("%s.%s.%s", parsed.Major, parsed.Minor, parsed.Patch)
	report.Version.Obj = *parsed
	return report
}

func checkHTTPStatus(baseReport types.ServiceReport) types.ServiceReport {
	report := baseReport
	host := report.Domain
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%s/service", host, report.Port)
	jsonResp, err := utils.FetchURL(url, 2*time.Second)
	if err != nil {
		report.Status = "offline"
		return report
	}
	var health utils.ServiceReport
	if err := json.Unmarshal([]byte(jsonResp), &health); err != nil {
		report.Status = "offline"
		return report
	}
	report.Status = "online"
	if strings.ToLower(health.Health.Status) != "ok" {
		report.Status = "offline"
	}
	report.Uptime = health.Health.Uptime
	report.Version = health.Version
	report.HealthMessage = health.Health.Message
	if cpu, ok := health.Metrics["cpu"].(map[string]interface{}); ok {
		if val, ok := cpu["avg"].(float64); ok {
			report.CPU = fmt.Sprintf("%.1f%%", val)
		}
	}
	if mem, ok := health.Metrics["memory"].(map[string]interface{}); ok {
		if val, ok := mem["avg"].(float64); ok {
			report.Memory = fmt.Sprintf("%.1f MB", val)
		}
	}
	return report
}

func checkRedisStatus(baseReport types.ServiceReport, creds *config.ServiceCredentials, isPublic bool) types.ServiceReport {
	report := baseReport
	if strings.Contains(strings.ToLower(report.ID), "upstash") {
		report.Status = "online"
		report.Uptime = "∞"
		report.CPU = "0.2%"
		report.Memory = "1.4%"
		report.Version.Str = "Cloud"
		return report
	}
	host := report.Domain
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%s", host, report.Port)
	var pass string
	if creds != nil {
		pass = creds.Password
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: pass, DialTimeout: 2 * time.Second})
	defer func() { _ = rdb.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		report.Status = "offline"
		return report
	}
	infoStr, _ := rdb.Info(ctx, "server", "memory", "cpu").Result()
	info := parseRedisInfo(infoStr)
	report.Status = "online"
	if v, ok := info["redis_version"]; ok {
		report.Version.Str = v
	}
	if u, ok := info["uptime_in_seconds"]; ok {
		if s, err := strconv.ParseInt(u, 10, 64); err == nil {
			report.Uptime = formatSecondsToUptime(s)
		}
	}
	if isLocalAddress(report.Domain) {
		report.CPU = getSystemdServiceCPU("redis")
		report.Memory = getSystemdServiceMemory("redis")
	}
	return report
}

func checkProdStatus(baseReport types.ServiceReport) types.ServiceReport {
	report := baseReport
	url := fmt.Sprintf("https://%s", report.Domain)
	resp, err := http.Get(url)
	if err != nil {
		report.Status = "offline"
		return report
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		report.Status = "online"
	} else {
		report.Status = "offline"
	}
	report.Uptime = "∞"
	report.Version.Str = "Cloud"
	return report
}

func parseRedisInfo(info string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(info, "\r\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func formatSecondsToUptime(seconds int64) string {
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if d > 0 {
		return fmt.Sprintf("%dd%dh%dm%ds", d, h, m, s)
	}
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func getCategoryFromType(eventType string) string {
	categories := map[string][]string{
		"messaging":  {"message_received", "message_sent", "messaging.user.sent_message", "messaging.bot.sent_message", "messaging.user.transcribed", "voice_transcribed", "messaging.user.joined_voice", "messaging.user.left_voice", "messaging.webhook.message"},
		"system":     {"system.cli.command", "system.cli.status", "system.status.change", "metric_recorded", "log_entry", "error_occurred", "webhook.processed", "messaging.bot.status_update", "messaging.user.joined_server", "system.test.completed", "system.build.completed", "system.roadmap.created", "system.roadmap.updated", "system.process.registered", "system.process.unregistered"},
		"cognitive":  {"engagement.decision", "system.analysis.audit", "system.blueprint.generated", "analysis.link.completed", "analysis.visual.completed", "analysis.router.decision", "analysis.user.message_signals"},
		"moderation": {"moderation.explicit_content.deleted"},
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

func GetSystemOptionsSnapshot() map[string]interface{} {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, "Dexter", "config", "options.json"))
	if err != nil {
		return nil
	}
	var opts struct {
		Services  map[string]map[string]interface{} `json:"services"`
		Cognitive map[string]interface{}            `json:"cognitive"`
	}
	if err := json.Unmarshal(data, &opts); err != nil {
		return nil
	}
	res := make(map[string]interface{})
	if opts.Services != nil {
		res["services"] = opts.Services
	}
	if opts.Cognitive != nil {
		res["cognitive"] = opts.Cognitive
	}
	return res
}

func CleanupAlertsHandler(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}
	ctx := context.Background()
	ids, _ := RDB.ZRevRange(ctx, "events:type:system.notification.generated", 0, -1).Result()
	deleted := 0
	for _, id := range ids {
		val, err := RDB.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}
		var event types.Event
		if err := json.Unmarshal([]byte(val), &event); err == nil {
			var ed map[string]interface{}
			if err := json.Unmarshal(event.Event, &ed); err == nil {
				if title, _ := ed["title"].(string); strings.HasPrefix(title, prefix) {
					RDB.Del(ctx, "event:"+id)
					RDB.ZRem(ctx, "events:timeline", id)
					RDB.ZRem(ctx, "events:type:system.notification.generated", id)
					RDB.ZRem(ctx, "events:service:"+event.Service, id)
					deleted++
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "deleted": deleted})
}
