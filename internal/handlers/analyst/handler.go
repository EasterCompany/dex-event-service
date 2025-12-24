package analyst

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv" // Added for strconv.ParseInt
	"strings" // Added for strings.Builder
	"time"

	"github.com/EasterCompany/dex-event-service/internal/discord" // Corrected import
	"github.com/EasterCompany/dex-event-service/internal/ollama"  // Corrected import
	"github.com/EasterCompany/dex-event-service/internal/web"     // Corrected import
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
	IdleDuration = 15 * time.Minute
	// MaxEventsToAnalyze is the maximum number of recent events to feed to the LLM
	MaxEventsToAnalyze = 500
	// LastActivityKey is the Redis key to store the timestamp of the last user-facing activity
	LastActivityKey = "analyst:last_activity_ts"
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
	OllamaClient   *ollama.Client     // Corrected type
	DiscordClient  *discord.Client    // Corrected type
	WebClient      *web.Client        // Corrected type
	CancelFunc     context.CancelFunc // To gracefully stop the worker goroutine
	lastAnalyzedTS int64              // Timestamp of the last event processed by analysis
}

// NewAnalystHandler creates a new AnalystHandler instance.
func NewAnalystHandler(redisClient *redis.Client, ollamaClient *ollama.Client, discordClient *discord.Client, webClient *web.Client) *AnalystHandler {
	return &AnalystHandler{
		RedisClient:    redisClient,
		OllamaClient:   ollamaClient,
		DiscordClient:  discordClient,
		WebClient:      webClient,
		lastAnalyzedTS: time.Now().Add(-1 * time.Hour).Unix(), // Look back 1 hour on first run if no state
	}
}

// Init initializes the handler, setting up its state and starting background routines.
func (h *AnalystHandler) Init(ctx context.Context) error {
	log.Printf("[%s] Initializing handler...", HandlerName)

	// Recover last analysis timestamp from Redis
	lastAnalysisStr, err := h.RedisClient.Get(ctx, LastAnalysisKey).Result()
	if err == nil {
		if ts, err := strconv.ParseInt(lastAnalysisStr, 10, 64); err == nil {
			h.lastAnalyzedTS = ts
		}
	}
	log.Printf("[%s] Last analysis coverage up to: %s", HandlerName, time.Unix(h.lastAnalyzedTS, 0).Format(time.RFC3339))

	// Start the background worker
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

// HandleEvent processes incoming events (now a no-op as we query the timeline directly for idle checks).
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
	h.RedisClient.Set(ctx, key, jsonBytes, 2*time.Minute) // 2m TTL as safety
}

// checkAndAnalyze determines if an analysis run is needed and executes it.
func (h *AnalystHandler) checkAndAnalyze(ctx context.Context) {
	timelineKey := "events:timeline"

	// 1. Get the very last event in the system to determine TRUE idle time
	lastEventIDs, err := h.RedisClient.ZRevRangeWithScores(ctx, timelineKey, 0, 0).Result()
	if err != nil || len(lastEventIDs) == 0 {
		// If timeline is empty, nothing to do
		return
	}

	lastSystemActivityTS := int64(lastEventIDs[0].Score)
	idleTime := time.Since(time.Unix(lastSystemActivityTS, 0))

	// 2. Check for ACTIVE processes (cogntive tasks, builds, etc)
	// These are reported in Redis as process:info:<channel_id>
	iter := h.RedisClient.Scan(ctx, 0, "process:info:*", 0).Iterator()
	activeProcessCount := 0
	for iter.Next(ctx) {
		activeProcessCount++
	}

	if activeProcessCount > 0 {
		// log.Printf("[%s] Idle check: system has %d active processes. Resetting idle timer.", HandlerName, activeProcessCount)
		return
	}

	log.Printf("[%s] Idle check: system has been idle for %s (Threshold: %s)", HandlerName, idleTime.Round(time.Second), IdleDuration)

	// Check for idle state
	if idleTime < IdleDuration {
		return
	}

	// 3. Check if new events have occurred since our LAST SUCCESSFUL ANALYSIS
	newEventCount, err := h.RedisClient.ZCount(ctx, timelineKey, fmt.Sprintf("(%d", h.lastAnalyzedTS), "+inf").Result()
	if err != nil {
		log.Printf("[%s] Error counting new events: %v", HandlerName, err)
		return
	}

	if newEventCount == 0 {
		return
	}

	log.Printf("[%s] System idle threshold met. %d new events since last analysis. Initiating analysis...",
		HandlerName, newEventCount)

	// Report process status so it appears on dashboard
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

	// Update last analyzed timestamp to current time
	h.lastAnalyzedTS = time.Now().Unix()
	h.RedisClient.Set(ctx, LastAnalysisKey, h.lastAnalyzedTS, 0)
	log.Printf("[%s] Analysis coverage updated to %s.", HandlerName, time.Unix(h.lastAnalyzedTS, 0).Format(time.RFC3339))
}

// extractJSON attempts to find and return a JSON string within a larger text block,
// specifically handling markdown code blocks (```json ... ```).
func extractJSON(input string) string {
	input = strings.TrimSpace(input)

	// Check for markdown code blocks
	if strings.Contains(input, "```") {
		// Try to find ```json ... ```
		re := regexp.MustCompile("(?s)```(?:json)?\n?(.*?)\n?```")
		match := re.FindStringSubmatch(input)
		if len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
	}

	// Fallback to finding the first '[' or '{' and last ']' or '}'
	start := strings.IndexAny(input, "[{")
	end := strings.LastIndexAny(input, "]}")

	if start != -1 && end != -1 && end > start {
		return input[start : end+1]
	}

	return input
}

// PerformAnalysis fetches events, creates a prompt, calls Ollama, and parses notifications.
func (h *AnalystHandler) PerformAnalysis(ctx context.Context, sinceTS, untilTS int64) ([]Notification, error) {
	// Fetch events since last analysis
	events, err := h.fetchEventsForAnalysis(ctx, sinceTS, untilTS)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch events: %w", err)
	}

	if len(events) == 0 {
		return nil, nil // No events to analyze
	}

	prompt := h.buildAnalysisPrompt(events)

	// Call Ollama
	ollamaResponseString, err := h.OllamaClient.Generate(OllamaModel, prompt, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama generation failed: %w", err)
	}

	// Extract clean JSON from potential markdown/prose
	cleanJSON := extractJSON(ollamaResponseString)

	// 1. Try to parse as the expected wrapped object: {"notifications": [...]}
	var ollamaOutput struct {
		Notifications []Notification `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(cleanJSON), &ollamaOutput); err == nil && len(ollamaOutput.Notifications) > 0 {
		return ollamaOutput.Notifications, nil
	}

	// 2. Fallback: Try to parse as a raw array: [...]
	var rawArray []Notification
	if err := json.Unmarshal([]byte(cleanJSON), &rawArray); err == nil {
		return rawArray, nil
	}

	// 3. If both failed, return the error notification
	log.Printf("[%s] Warning: Ollama response was not valid JSON. Response: %s", HandlerName, ollamaResponseString)
	return []Notification{{
		Title:    "AI Analysis Failed",
		Priority: "low",
		Category: "system",
		Body:     fmt.Sprintf("AI failed to parse its own output. Error: Unrecognized JSON format.\n\nRaw output:\n%s", ollamaResponseString),
	}}, nil
}

// Notification struct for parsing LLM output
type Notification struct {
	Title           string   `json:"title"`
	Priority        string   `json:"priority"` // low, medium, high, critical
	Category        string   `json:"category"` // system, security, conversation, error, build
	Body            string   `json:"body"`
	RelatedEventIDs []string `json:"related_event_ids"`
	Read            bool     `json:"read"`
}

// fetchEventsForAnalysis retrieves events from Redis for analysis.
func (h *AnalystHandler) fetchEventsForAnalysis(ctx context.Context, sinceTS, untilTS int64) ([]types.Event, error) {
	timelineKey := "events:timeline"
	// Fetch events within the time range, up to MaxEventsToAnalyze, ordered newest first
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
			log.Printf("[%s] Warning: Could not retrieve event data for ID: %s, Error: %v", HandlerName, cmd.Args()[1], err)
			continue
		}
		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			log.Printf("[%s] Warning: Could not unmarshal event JSON: %s, Error: %v", HandlerName, eventJSON, err)
			continue
		}
		// Exclude notification events themselves from being re-analyzed to prevent feedback loops
		var eventData map[string]interface{}
		if err := json.Unmarshal(event.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)

			// 1. Skip already generated notifications to prevent feedback loops
			if eventType == string(types.EventTypeSystemNotificationGenerated) {
				continue
			}

			// 2. Skip "noise" events (speaking started/stopped, etc)
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

// buildAnalysisPrompt constructs the prompt for the Ollama LLM.
func (h *AnalystHandler) buildAnalysisPrompt(events []types.Event) string {
	// System prompt based on the user's request
	systemPrompt := fmt.Sprintf("%s\n\n%s\n\nYour task is to review a series of event logs and identify critical errors, unusual situations, significant completed workflows, or notable user interactions. Summarize these into concise, actionable notifications for the master user.", utils.DexterIdentity, utils.DexterArchitecture)

	instructions := `
**Instructions:**
- Analyze the provided events chronologically.
- Focus on patterns, anomalies, and changes in system state or user behavior.
- Ignore repetitive, benign events (e.g., routine status checks, minor logs unless they form a pattern).
- For each identified insight, generate a single notification in JSON format.
- Notifications should be clear, concise, and provide context.
- Your response must be a JSON array of notifications.
- Use the following JSON schema for each notification object:
{
  "notifications": [
    {
      "title": "Concise summary (e.g., 'Build Failed Repeatedly')",
      "priority": "low|medium|high|critical",
      "category": "system|security|conversation|error|build|user_activity",
      "body": "Detailed explanation, patterns observed, or suggested actions (e.g., 'The 'dex-cli' failed to build 3 times due to linting errors. Check 'service_client.go'.')",
      "related_event_ids": ["uuid-1", "uuid-2"] // List of relevant event IDs from the input logs
    }
  ]
}
- If no significant patterns or critical insights are found, return an empty JSON object with an empty "notifications" array: {"notifications": []}.
- Ensure the JSON is well-formed and strictly adheres to the schema.
`

	// User content: formatted event logs
	var eventLogBuilder strings.Builder
	eventLogBuilder.WriteString("Event Logs:\n")
	eventLogBuilder.WriteString("-------------\n")

	for _, event := range events {
		var eventData map[string]interface{}
		_ = json.Unmarshal(event.Event, &eventData) // Safe to ignore error, handled by prompt.

		eventType, _ := eventData["type"].(string)
		summary := templates.FormatEventAsText(eventType, eventData, event.Service, event.Timestamp, 0, "UTC", "en") // Use generic formatting
		eventLogBuilder.WriteString(fmt.Sprintf("%s | %s | %s\n", time.Unix(event.Timestamp, 0).Format("15:04:05"), event.Service, summary))
	}
	eventLogBuilder.WriteString("-------------\n")
	eventLogBuilder.WriteString("Based on the above events, please generate a JSON array of notifications. Ensure the output is JUST the JSON, no prose.\n")

	return systemPrompt + "\n" + instructions + "\n" + eventLogBuilder.String()
}

// emitNotification creates and stores a new system.notification.generated event.
func (h *AnalystHandler) emitNotification(ctx context.Context, notif Notification) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()

	// Add type and timestamp to notification payload for template validation
	notifPayload := map[string]interface{}{
		"type":              string(types.EventTypeSystemNotificationGenerated),
		"title":             notif.Title,
		"priority":          notif.Priority,
		"category":          notif.Category,
		"body":              notif.Body,
		"related_event_ids": notif.RelatedEventIDs,
		"read":              false, // Always emit as unread
	}

	eventJSON, err := json.Marshal(notifPayload)
	if err != nil {
		log.Printf("[%s] Error marshaling notification payload: %v", HandlerName, err)
		return
	}

	event := types.Event{
		ID:        eventID,
		Service:   HandlerName, // Source of the notification
		Event:     eventJSON,
		Timestamp: timestamp,
	}

	// Marshal the full event for storage
	fullEventJSON, err := json.Marshal(event)
	if err != nil {
		log.Printf("[%s] Error marshaling full event: %v", HandlerName, err)
		return
	}

	// Use Redis pipeline for atomic operations
	pipe := h.RedisClient.Pipeline()

	eventKey := "event:" + eventID
	pipe.Set(ctx, eventKey, fullEventJSON, 0) // Store the full notification event struct

	// Add event ID to the global sorted set (timeline)
	pipe.ZAdd(ctx, "events:timeline", redis.Z{
		Score:  float64(timestamp),
		Member: eventID,
	})

	// Add event ID to a service-specific sorted set
	pipe.ZAdd(ctx, "events:service:"+HandlerName, redis.Z{
		Score:  float64(timestamp),
		Member: eventID,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[%s] Error storing generated notification event: %v", HandlerName, err)
	} else {
		log.Printf("[%s] Emitted new notification: \"%s\" (Priority: %s)", HandlerName, notif.Title, notif.Priority)
	}
}
