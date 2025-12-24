package analyst

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// HandlerName is the name of this handler
	HandlerName = "analyst"
	// AnalysisInterval is how often the analyst checks for idle state
	AnalysisInterval = 1 * time.Minute
	// IdleDuration is how long the system must be idle before analysis
	IdleDuration = 5 * time.Minute
	// MaxEventsToAnalyze is the maximum number of recent events to feed to the LLM
	MaxEventsToAnalyze = 500
	// MaxLogsToAnalyze is the number of recent log lines per service to analyze
	MaxLogsToAnalyze = 20
	// LastAnalysisKey is the Redis key to store the timestamp of the last successful analysis
	LastAnalysisKey = "analyst:last_analysis_ts"
	// OllamaModel is the model to use for analysis
	OllamaModel = "dex-analyst-model"
)

var ignoredEventTypes = []string{
	"messaging.user.speaking.started",
	"messaging.user.speaking.stopped",
	"voice_speaking_started",
	"voice_speaking_stopped",
}

// AnalystHandler handles generating proactive notifications based on event timeline analysis.
type AnalystHandler struct {
	RedisClient    *redis.Client
	OllamaClient   *ollama.Client
	DiscordClient  *discord.Client
	WebClient      *web.Client
	CancelFunc     context.CancelFunc
	lastAnalyzedTS int64
}

// NewAnalystHandler creates a new AnalystHandler instance.
func NewAnalystHandler(redisClient *redis.Client, ollamaClient *ollama.Client, discordClient *discord.Client, webClient *web.Client) *AnalystHandler {
	return &AnalystHandler{
		RedisClient:    redisClient,
		OllamaClient:   ollamaClient,
		DiscordClient:  discordClient,
		WebClient:      webClient,
		lastAnalyzedTS: time.Now().Add(-1 * time.Hour).Unix(),
	}
}

// Init initializes the handler, setting up its state and starting background routines.
func (h *AnalystHandler) Init(ctx context.Context) error {
	log.Printf("[%s] Initializing handler...", HandlerName)

	lastAnalysisStr, err := h.RedisClient.Get(ctx, LastAnalysisKey).Result()
	if err == nil {
		if ts, err := strconv.ParseInt(lastAnalysisStr, 10, 64); err == nil {
			h.lastAnalyzedTS = ts
		}
	}
	log.Printf("[%s] Last analysis coverage up to: %s", HandlerName, time.Unix(h.lastAnalyzedTS, 0).Format(time.RFC3339))

	ctx, cancel := context.WithCancel(context.Background())
	h.CancelFunc = cancel
	go h.runWorker(ctx)

	log.Printf("[%s] Handler initialized.", HandlerName)
	return nil
}

// Close stops any background routines.
func (h *AnalystHandler) Close() error {
	log.Printf("[%s] Closing handler...", HandlerName)
	if h.CancelFunc != nil {
		h.CancelFunc()
	}
	log.Printf("[%s] Handler closed.", HandlerName)
	return nil
}

// HandleEvent processes incoming events (no-op).
func (h *AnalystHandler) HandleEvent(ctx context.Context, event *types.Event, eventData map[string]interface{}) ([]types.Event, error) {
	return nil, nil
}

// runWorker is the main loop for the analyst handler's background operations.
func (h *AnalystHandler) runWorker(ctx context.Context) {
	ticker := time.NewTicker(AnalysisInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] Worker stopped.", HandlerName)
			return
		case <-ticker.C:
			h.checkAndAnalyze(ctx)
		}
	}
}

// reportProcessStatus updates the analyst's current state in Redis for dashboard visibility.
func (h *AnalystHandler) reportProcessStatus(ctx context.Context, state string) {
	key := "process:info:system-analyst"
	data := map[string]interface{}{
		"channel_id": "system-analyst",
		"state":      state,
		"retries":    0,
		"start_time": time.Now().Unix(),
		"pid":        os.Getpid(),
		"updated_at": time.Now().Unix(),
	}

	jsonBytes, _ := json.Marshal(data)
	h.RedisClient.Set(ctx, key, jsonBytes, 2*time.Minute)
}

// checkAndAnalyze determines if an analysis run is needed and executes it.
func (h *AnalystHandler) checkAndAnalyze(ctx context.Context) {
	timelineKey := "events:timeline"

	lastEvents, err := h.RedisClient.ZRevRangeWithScores(ctx, timelineKey, 0, 10).Result()
	if err != nil || len(lastEvents) == 0 {
		return
	}

	var lastSystemActivityTS int64
	foundActivity := false

	for _, z := range lastEvents {
		eventID := z.Member.(string)
		eventJSON, err := h.RedisClient.Get(ctx, "event:"+eventID).Result()
		if err != nil {
			continue
		}

		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			continue
		}

		var eventData map[string]interface{}
		if err := json.Unmarshal(event.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)
			if eventType == string(types.EventTypeSystemNotificationGenerated) {
				continue
			}
		}

		lastSystemActivityTS = int64(z.Score)
		foundActivity = true
		break
	}

	if !foundActivity {
		lastSystemActivityTS = int64(lastEvents[0].Score)
	}

	idleTime := time.Since(time.Unix(lastSystemActivityTS, 0))

	iter := h.RedisClient.Scan(ctx, 0, "process:info:*", 0).Iterator()
	activeProcessCount := 0
	for iter.Next(ctx) {
		activeProcessCount++
	}

	if activeProcessCount > 0 {
		return
	}

	log.Printf("[%s] Idle check: system has been idle for %s (Threshold: %s)", HandlerName, idleTime.Round(time.Second), IdleDuration)

	if idleTime < IdleDuration {
		return
	}

	newEventCount, err := h.RedisClient.ZCount(ctx, timelineKey, fmt.Sprintf("(%d", h.lastAnalyzedTS), "+inf").Result()
	if err != nil {
		log.Printf("[%s] Error counting new events: %v", HandlerName, err)
		return
	}

	if newEventCount == 0 {
		return
	}

	log.Printf("[%s] System idle threshold met. %d new events since last analysis. Initiating analysis...", HandlerName, newEventCount)

	h.reportProcessStatus(ctx, fmt.Sprintf("Analyzing %d events", newEventCount))
	defer h.RedisClient.Del(ctx, "process:info:system-analyst")

	notifications, err := h.PerformAnalysis(ctx, h.lastAnalyzedTS, time.Now().Unix())
	if err != nil {
		log.Printf("[%s] Error during analysis: %v", HandlerName, err)
		return
	}

	if len(notifications) == 0 {
		log.Printf("[%s] Analysis completed: No significant patterns found.", HandlerName)
		h.emitNotification(ctx, Notification{
			Title:    "Analysis Completed",
			Priority: "low",
			Category: "system",
			Body:     fmt.Sprintf("Analyzed %d events. No significant patterns or anomalies were detected during this idle period.", newEventCount),
		})
	} else {
		for _, notif := range notifications {
			h.emitNotification(ctx, notif)
		}
	}

	latestEventInSet, err := h.RedisClient.ZRevRangeWithScores(ctx, timelineKey, 0, 0).Result()
	if err == nil && len(latestEventInSet) > 0 {
		h.lastAnalyzedTS = int64(latestEventInSet[0].Score)
	} else {
		h.lastAnalyzedTS = time.Now().Unix()
	}

	h.RedisClient.Set(ctx, LastAnalysisKey, h.lastAnalyzedTS, 0)
	log.Printf("[%s] Analysis coverage updated to %s.", HandlerName, time.Unix(h.lastAnalyzedTS, 0).Format(time.RFC3339))
}

// extractJSON attempts to find and return a JSON string within a larger text block.
func extractJSON(input string) string {
	input = strings.TrimSpace(input)
	if strings.Contains(input, "```") {
		re := regexp.MustCompile("(?s)```(?:json)?\n?(.*?)\n?```")
		match := re.FindStringSubmatch(input)
		if len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
	}
	start := strings.IndexAny(input, "[[")
	end := strings.LastIndexAny(input, "]]")
	if start != -1 && end != -1 && end > start {
		return input[start : end+1]
	}
	return input
}

// PerformAnalysis fetches events, creates a prompt, calls Ollama, and parses notifications.
func (h *AnalystHandler) PerformAnalysis(ctx context.Context, sinceTS, untilTS int64) ([]Notification, error) {
	events, err := h.fetchEventsForAnalysis(ctx, sinceTS, untilTS)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch events: %w", err)
	}
	if len(events) == 0 {
		return nil, nil
	}

	status, _ := h.fetchSystemStatus()
	logs, _ := h.fetchRecentLogs()
	history, err := h.fetchRecentNotifications(ctx, 20)
	if err != nil {
		log.Printf("[%s] Warning: Failed to fetch history context: %v", HandlerName, err)
	}

	prompt := h.buildAnalysisPrompt(events, history, status, logs)

	ollamaResponseString, err := h.OllamaClient.Generate(OllamaModel, prompt, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama generation failed: %w", err)
	}

	cleanJSON := extractJSON(ollamaResponseString)

	var ollamaOutput struct {
		Notifications []Notification `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(cleanJSON), &ollamaOutput); err == nil && len(ollamaOutput.Notifications) > 0 {
		return ollamaOutput.Notifications, nil
	}

	var rawArray []Notification
	if err := json.Unmarshal([]byte(cleanJSON), &rawArray); err == nil && len(rawArray) > 0 {
		var validNotifications []Notification
		for _, n := range rawArray {
			if n.Title != "" || n.Body != "" {
				validNotifications = append(validNotifications, n)
			}
		}
		if len(validNotifications) > 0 {
			return validNotifications, nil
		}
	}

	return nil, nil
}

// fetchRecentNotifications retrieves the last N notifications generated by the system.
func (h *AnalystHandler) fetchRecentNotifications(ctx context.Context, count int) ([]Notification, error) {
	serviceKey := "events:service:" + HandlerName
	eventIDs, err := h.RedisClient.ZRevRange(ctx, serviceKey, 0, int64(count-1)).Result()
	if err != nil {
		return nil, err
	}

	history := make([]Notification, 0, len(eventIDs))
	for _, id := range eventIDs {
		data, err := h.RedisClient.Get(ctx, "event:"+id).Result()
		if err != nil {
			continue
		}
		var event types.Event
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(event.Event, &payload); err != nil {
			continue
		}
		if payload["type"] != string(types.EventTypeSystemNotificationGenerated) {
			continue
		}
		history = append(history, Notification{
			Title:    payload["title"].(string),
			Priority: payload["priority"].(string),
			Category: payload["category"].(string),
			Body:     payload["body"].(string),
		})
	}
	return history, nil
}

// fetchSystemStatus retrieves current health of all services
func (h *AnalystHandler) fetchSystemStatus() ([]types.ServiceReport, error) {
	configuredServices, err := config.LoadServiceMap()
	if err != nil {
		return nil, err
	}

	var reports []types.ServiceReport
	for group, services := range configuredServices.Services {
		for _, s := range services {
			status := "unknown"
			if s.Port != "" {
				url := fmt.Sprintf("http://localhost:%s/service", s.Port)
				_, err := utils.FetchURL(url, 500*time.Millisecond)
				if err == nil {
					status = "online"
				} else {
					status = "offline"
				}
			}
			reports = append(reports, types.ServiceReport{
				ID:     s.ID,
				Type:   group,
				Status: status,
			})
		}
	}
	return reports, nil
}

// fetchRecentLogs retrieves recent log lines for all manageable services
func (h *AnalystHandler) fetchRecentLogs() ([]types.LogReport, error) {
	configuredServices, err := config.LoadServiceMap()
	if err != nil {
		return nil, err
	}

	var reports []types.LogReport
	home := os.Getenv("HOME")

	for _, services := range configuredServices.Services {
		for _, s := range services {
			if strings.Contains(s.ID, "cli") || strings.Contains(s.ID, "os") {
				continue
			}
			logPath := fmt.Sprintf("%s/Dexter/logs/%s.log", home, s.ID)
			lines, _ := h.readLastNLines(logPath, MaxLogsToAnalyze)
			if len(lines) > 0 {
				reports = append(reports, types.LogReport{
					ID:   s.ID,
					Logs: lines,
				})
			}
		}
	}
	return reports, nil
}

func (h *AnalystHandler) readLastNLines(filePath string, n int) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, scanner.Err()
}

// buildAnalysisPrompt constructs the prompt for the Ollama LLM.
func (h *AnalystHandler) buildAnalysisPrompt(events []types.Event, history []Notification, status []types.ServiceReport, logs []types.LogReport) string {
	systemPrompt := fmt.Sprintf("%s\n\n%s\n\nYour task is to act as a Tier 1 Technical Analyst. You will monitor system logs, events, and service status for technical issues, bugs, service failures, or build errors. You must report these as notifications for the master user.", utils.DexterIdentity, utils.DexterArchitecture)

	instructions := `
**Primary Objective (Tier 1 Analysis):**
- Identify technical issues, service crashes, repeated build failures, or API timeouts.
- Analyze the provided Status, Logs, and Events together to find root causes.
- Focus strictly on technical health and reliability.

**Memory & Continuity:**
- You will be provided with a list of "Recent Reported Issues." 
- **DO NOT report the same issue multiple times.**
- If an issue is already in the history and persists, only report it again if the severity has increased or the error message has changed.

**Output Constraints:**
- If no NEW technical issues are found, return exactly: {"notifications": []}.
- Your output must consist ONLY of the JSON object. No prose.

**JSON Schema:**
{
  "notifications": [
    {
      "title": "Clear summary of the technical issue",
      "priority": "low|medium|high|critical",
      "category": "error|build|system|security",
      "body": "Detailed explanation, including service names and root cause analysis from logs/events.",
      "related_event_ids": ["uuid-1"]
    }
  ]
}
`

	var sb strings.Builder
	sb.WriteString(systemPrompt + "\n")
	sb.WriteString(instructions + "\n")

	if len(history) > 0 {
		sb.WriteString("\n### Recent Reported Issues (Memory):\n")
		for _, h := range history {
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", h.Priority, h.Title, h.Body))
		}
	}

	if len(status) > 0 {
		sb.WriteString("\n### Current System Status:\n")
		for _, s := range status {
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", s.ID, s.Type, s.Status))
		}
	}

	if len(logs) > 0 {
		sb.WriteString("\n### Recent System Logs:\n")
		for _, report := range logs {
			sb.WriteString(fmt.Sprintf("[%s]:\n", report.ID))
			for _, line := range report.Logs {
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}

	sb.WriteString("\n### New Event Logs:\n")
	for _, event := range events {
		var eventData map[string]interface{}
		_ = json.Unmarshal(event.Event, &eventData)
		eventType, _ := eventData["type"].(string)
		summary := templates.FormatEventAsText(eventType, eventData, event.Service, event.Timestamp, 0, "UTC", "en")
		sb.WriteString(fmt.Sprintf("%s | %s | %s\n", time.Unix(event.Timestamp, 0).Format("15:04:05"), event.Service, summary))
	}

	return sb.String()
}

// Notification struct for parsing LLM output
type Notification struct {
	Title           string   `json:"title"`
	Priority        string   `json:"priority"`
	Category        string   `json:"category"`
	Body            string   `json:"body"`
	RelatedEventIDs []string `json:"related_event_ids"`
	Read            bool     `json:"read"`
}

// fetchEventsForAnalysis retrieves events from Redis for analysis.
func (h *AnalystHandler) fetchEventsForAnalysis(ctx context.Context, sinceTS, untilTS int64) ([]types.Event, error) {
	timelineKey := "events:timeline"
	eventIDs, err := h.RedisClient.ZRevRangeByScore(ctx, timelineKey, &redis.ZRangeBy{
		Min:   fmt.Sprintf("%d", sinceTS),
		Max:   fmt.Sprintf("%d", untilTS),
		Count: MaxEventsToAnalyze,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	events := make([]types.Event, 0, len(eventIDs))
	pipe := h.RedisClient.Pipeline()
	cmds := make([]*redis.StringCmd, len(eventIDs))
	for i, eventID := range eventIDs {
		cmds[i] = pipe.Get(ctx, "event:"+eventID)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return nil, err
	}
	for _, cmd := range cmds {
		eventJSON, err := cmd.Result()
		if err != nil {
			continue
		}
		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			continue
		}
		var eventData map[string]interface{}
		if err := json.Unmarshal(event.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)
			if eventType == string(types.EventTypeSystemNotificationGenerated) {
				continue
			}
			isIgnored := false
			for _, ignored := range ignoredEventTypes {
				if eventType == ignored {
					isIgnored = true
					break
				}
			}
			if isIgnored {
				continue
			}
		}
		events = append(events, event)
	}
	return events, nil
}

// emitNotification creates and stores a new system.notification.generated event.
func (h *AnalystHandler) emitNotification(ctx context.Context, notif Notification) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()
	notifPayload := map[string]interface{}{
		"type":              string(types.EventTypeSystemNotificationGenerated),
		"title":             notif.Title,
		"priority":          notif.Priority,
		"category":          notif.Category,
		"body":              notif.Body,
		"related_event_ids": notif.RelatedEventIDs,
		"read":              false,
	}
	eventJSON, err := json.Marshal(notifPayload)
	if err != nil {
		return
	}
	event := types.Event{
		ID:        eventID,
		Service:   HandlerName,
		Event:     eventJSON,
		Timestamp: timestamp,
	}
	fullEventJSON, err := json.Marshal(event)
	if err != nil {
		return
	}
	pipe := h.RedisClient.Pipeline()
	eventKey := "event:" + eventID
	pipe.Set(ctx, eventKey, fullEventJSON, 0)
	pipe.ZAdd(ctx, "events:timeline", redis.Z{Score: float64(timestamp), Member: eventID})
	pipe.ZAdd(ctx, "events:service:"+HandlerName, redis.Z{Score: float64(timestamp), Member: eventID})
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[%s] Error storing notification: %v", HandlerName, err)
	} else {
		log.Printf("[%s] Emitted: \"%s\"", HandlerName, notif.Title)
	}
}
