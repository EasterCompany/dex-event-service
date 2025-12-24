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
	lastCheckTS    int64
}

// NewAnalystHandler creates a new AnalystHandler instance.
func NewAnalystHandler(redisClient *redis.Client, ollamaClient *ollama.Client, discordClient *discord.Client, webClient *web.Client) *AnalystHandler {
	now := time.Now().Unix()
	return &AnalystHandler{
		RedisClient:    redisClient,
		OllamaClient:   ollamaClient,
		DiscordClient:  discordClient,
		WebClient:      webClient,
		lastAnalyzedTS: now - 3600,
		lastCheckTS:    now,
	}
}

// Init initializes the handler, setting up its state and starting background routines.
func (h *AnalystHandler) Init(ctx context.Context) error {
	log.Printf("[%s] Initializing handler...", HandlerName)

	h.lastCheckTS = time.Now().Unix()
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
			// Ignore our own notifications when calculating idle time
			if eventType == string(types.EventTypeSystemNotificationGenerated) || eventType == string(types.EventTypeSystemBlueprintGenerated) {
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

	// The idle timer starts from the LATEST of:
	// 1. The last significant user/system event
	// 2. The last time the analyst completed a run
	effectiveLastActivityTS := lastSystemActivityTS
	if h.lastCheckTS > effectiveLastActivityTS {
		effectiveLastActivityTS = h.lastCheckTS
	}

	idleTime := time.Since(time.Unix(effectiveLastActivityTS, 0))

	// Check if any cognitive processes are currently running
	iter := h.RedisClient.Scan(ctx, 0, "process:info:*", 0).Iterator()
	activeProcessCount := 0
	for iter.Next(ctx) {
		activeProcessCount++
	}

	if activeProcessCount > 0 {
		// If the system is busy, reset our check timer so we don't count "idle" while a job is running
		h.lastCheckTS = time.Now().Unix()
		return
	}

	log.Printf("[%s] Idle check: system has been idle for %s (Threshold: %s)", HandlerName, idleTime.Round(time.Second), IdleDuration)

	if idleTime < IdleDuration {
		return
	}

	// Reset check timer immediately to avoid double-triggers
	h.lastCheckTS = time.Now().Unix()

	newEventCount, err := h.RedisClient.ZCount(ctx, timelineKey, fmt.Sprintf("(%d", h.lastAnalyzedTS), "+inf").Result()
	if err != nil {
		log.Printf("[%s] Error counting new events: %v", HandlerName, err)
		return
	}

	log.Printf("[%s] System idle threshold met. %d new events since last analysis. Initiating analysis...", HandlerName, newEventCount)

	h.reportProcessStatus(ctx, "Running automated tests")
	defer h.RedisClient.Del(ctx, "process:info:system-analyst")

	// Even if newEventCount is 0, we still perform analysis to check Logs and Service Status (Tier 1)
	results, err := h.PerformAnalysis(ctx, h.lastAnalyzedTS, time.Now().Unix())
	if err != nil {
		log.Printf("[%s] Error during analysis: %v", HandlerName, err)
		return
	}

	if len(results) > 0 {
		h.reportProcessStatus(ctx, fmt.Sprintf("Emitting %d results", len(results)))
		for _, res := range results {
			h.emitResult(ctx, res)
		}
	}

	// Update coverage
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
	start := strings.Index(input, "[")
	end := strings.LastIndex(input, "]")
	if start != -1 && end != -1 && end > start {
		return input[start : end+1]
	}
	start = strings.Index(input, "{")
	end = strings.LastIndex(input, "}")
	if start != -1 && end != -1 && end > start {
		return input[start : end+1]
	}
	return input
}

// PerformAnalysis fetches events, creates prompts for each tier, and aggregates results.
func (h *AnalystHandler) PerformAnalysis(ctx context.Context, sinceTS, untilTS int64) ([]AnalysisResult, error) {
	events, err := h.fetchEventsForAnalysis(ctx, sinceTS, untilTS)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch events: %w", err)
	}

	status, _ := h.fetchSystemStatus()
	logs, _ := h.fetchRecentLogs()
	tests, _ := h.fetchTestResults()
	history, _ := h.fetchRecentNotifications(ctx, 20)

	var allResults []AnalysisResult

	// --- Tier 1: Guardian (Health & Stability) ---
	// Runs on every analysis cycle.
	h.reportProcessStatus(ctx, "Tier 1: Guardian Analysis")
	guardianPrompt := h.buildAnalysisPrompt(events, history, status, logs, tests, "guardian")
	log.Printf("[%s] Executing Tier 1 (Guardian) Analysis...", HandlerName)
	gResults, err := h.runAnalysisWithModel("dex-analyst-guardian", guardianPrompt)
	if err == nil {
		allResults = append(allResults, gResults...)
	}

	// --- Tier 2: Architect (Optimization & Quality) ---
	// Runs every 1 hour.
	lastArchitectRun, _ := h.RedisClient.Get(ctx, "analyst:last_run:architect").Result()
	lastArchTS, _ := strconv.ParseInt(lastArchitectRun, 10, 64)

	if time.Since(time.Unix(lastArchTS, 0)) >= 1*time.Hour {
		h.reportProcessStatus(ctx, "Tier 2: Architect Analysis")
		architectPrompt := h.buildAnalysisPrompt(events, history, status, logs, tests, "architect")
		log.Printf("[%s] Executing Tier 2 (Architect) Analysis...", HandlerName)
		aResults, err := h.runAnalysisWithModel("dex-analyst-architect", architectPrompt)
		if err == nil {
			allResults = append(allResults, aResults...)
			h.RedisClient.Set(ctx, "analyst:last_run:architect", time.Now().Unix(), 0)
		}
	}

	// --- Tier 3: Strategist (Feature Visionary) ---
	// Runs every 24 hours.
	lastStrategistRun, _ := h.RedisClient.Get(ctx, "analyst:last_run:strategist").Result()
	lastStratTS, _ := strconv.ParseInt(lastStrategistRun, 10, 64)

	if time.Since(time.Unix(lastStratTS, 0)) >= 24*time.Hour {
		h.reportProcessStatus(ctx, "Tier 3: Strategist Analysis")
		strategistPrompt := h.buildAnalysisPrompt(events, history, status, logs, tests, "strategist")
		log.Printf("[%s] Executing Tier 3 (Strategist) Analysis...", HandlerName)
		sResults, err := h.runAnalysisWithModel("dex-analyst-strategist", strategistPrompt)
		if err == nil {
			allResults = append(allResults, sResults...)
			h.RedisClient.Set(ctx, "analyst:last_run:strategist", time.Now().Unix(), 0)
		}
	}

	return allResults, nil
}

// runAnalysisWithModel executes a generation against a specific model and parses JSON.
func (h *AnalystHandler) runAnalysisWithModel(model, prompt string) ([]AnalysisResult, error) {
	ollamaResponseString, err := h.OllamaClient.Generate(model, prompt, nil)
	if err != nil {
		return nil, err
	}

	cleanJSON := extractJSON(ollamaResponseString)

	var ollamaOutput struct {
		Results []AnalysisResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(cleanJSON), &ollamaOutput); err == nil && len(ollamaOutput.Results) > 0 {
		return ollamaOutput.Results, nil
	}

	var rawArray []AnalysisResult
	if err := json.Unmarshal([]byte(cleanJSON), &rawArray); err == nil && len(rawArray) > 0 {
		var validResults []AnalysisResult
		for _, r := range rawArray {
			if r.Title != "" || r.Body != "" {
				validResults = append(validResults, r)
			}
		}
		return validResults, nil
	}

	return nil, nil
}

// fetchRecentNotifications retrieves the last N notifications generated by the system.
func (h *AnalystHandler) fetchRecentNotifications(ctx context.Context, count int) ([]AnalysisResult, error) {
	serviceKey := "events:service:" + HandlerName
	eventIDs, err := h.RedisClient.ZRevRange(ctx, serviceKey, 0, int64(count-1)).Result()
	if err != nil {
		return nil, err
	}

	history := make([]AnalysisResult, 0, len(eventIDs))
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

		resType := "notification"
		if payload["type"] == string(types.EventTypeSystemBlueprintGenerated) {
			resType = "blueprint"
		} else if payload["type"] != string(types.EventTypeSystemNotificationGenerated) {
			continue
		}

		history = append(history, AnalysisResult{
			Type:     resType,
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

// fetchTestResults runs 'dex test' if available and returns the raw output.
func (h *AnalystHandler) fetchTestResults() (string, error) {
	home := os.Getenv("HOME")
	dexPath := fmt.Sprintf("%s/Dexter/bin/dex", home)

	// Check if dex binary exists
	if _, err := os.Stat(dexPath); os.IsNotExist(err) {
		return "dex CLI not found, skipping automated tests.", nil
	}

	log.Printf("[%s] Running automated system tests via 'dex test'...", HandlerName)
	// Execute raw dex test - the output will be used for Tier 1 analysis
	cmd := dexPath + " test"
	output, err := utils.RunCommand(cmd)
	if err != nil {
		// We still return the output even on error so the Analyst can diagnose the failure
		return output, nil
	}

	return output, nil
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
func (h *AnalystHandler) buildAnalysisPrompt(events []types.Event, history []AnalysisResult, status []types.ServiceReport, logs []types.LogReport, tests string, tier string) string {
	// Use the specialized prompts from utils/system_context
	var systemPrompt string
	switch tier {
	case "guardian":
		systemPrompt = utils.GetAnalystGuardianPrompt()
	case "architect":
		systemPrompt = utils.GetAnalystArchitectPrompt()
	case "strategist":
		systemPrompt = utils.GetAnalystStrategistPrompt()
	default:
		systemPrompt = utils.GetAnalystGuardianPrompt()
	}

	var sb strings.Builder
	sb.WriteString(systemPrompt + "\n")

	if tests != "" {
		sb.WriteString("\n### Automated Test Results (dex test):\n")
		sb.WriteString(tests + "\n")
	}

	if len(history) > 0 {
		sb.WriteString("\n### Recent Reported Issues (Memory):\n")
		for _, h := range history {
			sb.WriteString(fmt.Sprintf("- [%s] [%s] %s: %s\n", h.Type, h.Priority, h.Title, h.Body))
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

	if len(events) > 0 {
		sb.WriteString("\n### New Event Logs:\n")
		for _, event := range events {
			var eventData map[string]interface{}
			_ = json.Unmarshal(event.Event, &eventData)
			eventType, _ := eventData["type"].(string)
			summary := templates.FormatEventAsText(eventType, eventData, event.Service, event.Timestamp, 0, "UTC", "en")
			sb.WriteString(fmt.Sprintf("%s | %s | %s\n", time.Unix(event.Timestamp, 0).Format("15:04:05"), event.Service, summary))
		}
	}

	return sb.String()
}

// AnalysisResult represents a single finding from the tiered reasoning loop.
// It can be either a 'notification' (Tier 1/2) or a 'blueprint' (Tier 3).
type AnalysisResult struct {
	Type            string   `json:"type"` // "notification" or "blueprint"
	Title           string   `json:"title"`
	Priority        string   `json:"priority"`
	Category        string   `json:"category"`
	Body            string   `json:"body"`
	RelatedEventIDs []string `json:"related_event_ids"`
	Read            bool     `json:"read"`

	// Blueprint-specific fields (Tier 3)
	Summary            string   `json:"summary,omitempty"`
	Content            string   `json:"content,omitempty"`
	AffectedServices   []string `json:"affected_services,omitempty"`
	ImplementationPath []string `json:"implementation_path,omitempty"`
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
			if eventType == string(types.EventTypeSystemNotificationGenerated) || eventType == string(types.EventTypeSystemBlueprintGenerated) {
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

// emitResult creates and stores a new system event (notification, alert, or blueprint).
func (h *AnalystHandler) emitResult(ctx context.Context, res AnalysisResult) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()

	var eventType string
	payload := map[string]interface{}{
		"title":             res.Title,
		"priority":          res.Priority,
		"category":          res.Category,
		"body":              res.Body,
		"related_event_ids": res.RelatedEventIDs,
		"read":              false,
	}

	switch res.Type {
	case "alert":
		eventType = string(types.EventTypeSystemNotificationAlert)
	case "blueprint":
		eventType = string(types.EventTypeSystemBlueprintGenerated)
		payload["summary"] = res.Summary
		payload["content"] = res.Content
		payload["affected_services"] = res.AffectedServices
		payload["implementation_path"] = res.ImplementationPath
	default:
		eventType = string(types.EventTypeSystemNotificationGenerated)
	}

	payload["type"] = eventType

	eventJSON, err := json.Marshal(payload)
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
		log.Printf("[%s] Error storing result: %v", HandlerName, err)
	} else {
		log.Printf("[%s] Emitted %s: \"%s\"", HandlerName, res.Type, res.Title)
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
	// We don't use expiration here; it's cleared manually by checkAndAnalyze
	h.RedisClient.Set(ctx, key, jsonBytes, 0)
}
