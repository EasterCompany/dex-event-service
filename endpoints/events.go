package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/handlers"
	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// Redis key patterns
	eventKeyPrefix    = "event:"
	timelineKey       = "events:timeline"
	defaultMaxResults = 100
)

// matchesEventFilters checks if an event's data matches the specified filters.
// It supports nested field access using dot notation (e.g., "user.id").
func matchesEventFilters(eventData json.RawMessage, filters map[string]string) bool {
	// Parse event data into a generic map
	var eventMap map[string]interface{}
	if err := json.Unmarshal(eventData, &eventMap); err != nil {
		return false // If we can't parse it, it doesn't match
	}

	// Check each filter
	for fieldPath, expectedValue := range filters {
		// Support nested fields with dot notation (e.g., "user.id")
		fields := strings.Split(fieldPath, ".")

		// Navigate through nested fields
		var currentValue interface{} = eventMap
		for _, field := range fields {
			if currentMap, ok := currentValue.(map[string]interface{}); ok {
				currentValue = currentMap[field]
			} else {
				// Field doesn't exist or isn't a map
				currentValue = nil // Treat as nil if path doesn't exist
				break
			}
		}

		// Convert the value to string for comparison
		actualValueStr := fmt.Sprintf("%v", currentValue)
		if actualValueStr == "<nil>" { // Normalize Go's nil representation
			actualValueStr = ""
		}

		// Support multiple comma-separated values (OR logic)
		expectedValues := strings.Split(expectedValue, ",")
		matchFound := false

		for _, val := range expectedValues {
			val = strings.TrimSpace(val)
			switch val {
			case "empty":
				if actualValueStr == "" || actualValueStr == "0" {
					matchFound = true
				}
			case "!empty":
				if actualValueStr != "" && actualValueStr != "0" {
					matchFound = true
				}
			default:
				if actualValueStr == val {
					matchFound = true
				}
			}
			if matchFound {
				break
			}
		}

		if !matchFound {
			return false
		}
	}

	return true
}

// EventsHandler routes requests to the appropriate handler based on method and path
func EventsHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract path after /events
		path := strings.TrimPrefix(r.URL.Path, "/events")
		path = strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodPost:
			if path == "" {
				CreateEventHandler(redisClient)(w, r)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		case http.MethodGet:
			if path == "" {
				GetTimelineHandler(redisClient)(w, r)
			} else {
				GetEventByIDHandler(redisClient, path)(w, r)
			}
		case http.MethodPatch:
			if path != "" {
				PatchEventHandler(redisClient, path)(w, r)
			} else {
				http.Error(w, "Event ID required for PATCH", http.StatusBadRequest)
			}
		case http.MethodDelete:
			if path != "" {
				DeleteEventHandler(redisClient, path)(w, r)
			} else {
				BulkDeleteEventHandler(redisClient)(w, r)
			}
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// BulkDeleteEventHandler deletes multiple events based on filters
func BulkDeleteEventHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		query := r.URL.Query()
		targetType := query.Get("type")
		category := query.Get("category")
		excludeTypes := query.Get("exclude_types")

		// Define categories matching frontend
		categories := map[string][]string{
			"messaging": {
				"message_received", "messaging.user.sent_message", "messaging.bot.sent_message",
				"messaging.user.transcribed", "voice_transcribed", "bot_response",
				"messaging.user.joined_voice", "messaging.user.left_voice",
				"messaging.bot.joined_voice", "messaging.bot.voice_response",
				"messaging.user.speaking.started", "messaging.user.speaking.stopped",
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
				"analysis.link.completed", "analysis.visual.completed",
			},
			"moderation": {
				"moderation.explicit_content.deleted",
			},
		}

		excludedTypesMap := make(map[string]bool)
		if excludeTypes != "" {
			for _, t := range strings.Split(excludeTypes, ",") {
				excludedTypesMap[strings.TrimSpace(t)] = true
			}
		}

		// ALWAYS exclude actual structural records from general clearing
		// They must be deleted via their specific windows or explicit type filtering
		if targetType == "" {
			excludedTypesMap[string(types.EventTypeSystemBlueprintGenerated)] = true
			excludedTypesMap[string(types.EventTypeSystemNotificationGenerated)] = true
		}

		targetTypes := make(map[string]bool)
		if targetType != "" {
			targetTypes[targetType] = true
		}
		if category != "" {
			if typesList, ok := categories[category]; ok {
				for _, t := range typesList {
					targetTypes[t] = true
				}
			}
		}

		// Get all events from timeline
		eventIDs, err := redisClient.ZRange(ctx, timelineKey, 0, -1).Result()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to retrieve timeline: %v", err), http.StatusInternalServerError)
			return
		}

		deletedCount := 0
		for _, eventID := range eventIDs {
			// Check type
			eventKey := eventKeyPrefix + eventID
			eventJSON, err := redisClient.Get(ctx, eventKey).Result()
			if err != nil {
				continue // Skip missing
			}

			var event types.Event
			if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
				continue
			}

			var eventData map[string]interface{}
			if err := json.Unmarshal(event.Event, &eventData); err != nil {
				continue
			}

			eventType, _ := eventData["type"].(string)

			// Skip if excluded via query param OR if it is a protected type
			if excludedTypesMap[eventType] {
				continue
			}

			// Optimization: If no filters, delete everything (that wasn't excluded)
			if targetType == "" && category == "" {
				if err := deleteEventByID(redisClient, ctx, eventID); err == nil {
					deletedCount++
				}
				continue
			}

			if targetTypes[eventType] {
				if err := deleteEventByID(redisClient, ctx, eventID); err == nil {
					deletedCount++
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "Deleted %d events", deletedCount)
	}
}

// CreateEventHandler creates a new event in Redis
func CreateEventHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()

		// Parse request body
		var req types.CreateEventRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
			return
		}

		// Validate request
		if req.Service == "" {
			http.Error(w, "Service field is required", http.StatusBadRequest)
			return
		}

		// Check for System Lock
		if IsEventLocked(req) {
			if err := QueuePendingEvent(redisClient, req); err != nil {
				http.Error(w, "System is locked and queue failed", http.StatusServiceUnavailable)
				return
			}
			// Return Accepted to indicate received but not processed immediately
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued", "reason": "system_locked"})
			return
		}

		if len(req.Event) == 0 {
			http.Error(w, "Event field is required", http.StatusBadRequest)
			return
		}

		// Parse event JSON to validate against template
		var eventData map[string]interface{}
		if err := json.Unmarshal(req.Event, &eventData); err != nil {
			http.Error(w, fmt.Sprintf("Invalid event JSON: %v", err), http.StatusBadRequest)
			return
		}

		// Extract event type
		eventTypeRaw, hasType := eventData["type"]
		if !hasType {
			http.Error(w, "Event must have a 'type' field", http.StatusBadRequest)
			return
		}

		eventType, ok := eventTypeRaw.(string)
		if !ok {
			http.Error(w, "Event 'type' field must be a string", http.StatusBadRequest)
			return
		}

		// --- COGNITIVE IDLE TIMER LOGIC ---
		// We track "Cognitive Idle Time" separately from raw "Last Event Time".
		// Status checks and monitoring events should NOT reset the cognitive timer.
		go func() {
			isCognitive := true

			// 1. Check Event Type
			ignoredTypes := []string{
				"system.cli.status",
				"system.monitor.hardware",
				"system.notification.generated", // Guardian generating notifications isn't a new cognitive input
				"system.analysis.audit",         // Guardian audit logs
				"system.attention.expired",      // Guardian forgetting context isn't a cognitive input
			}
			for _, t := range ignoredTypes {
				if eventType == t {
					isCognitive = false
					break
				}
			}

			// 2. Check CLI Commands (if applicable)
			if isCognitive && eventType == "system.cli.command" {
				if cmd, ok := eventData["command"].(string); ok {
					ignoredCmds := []string{"status", "logs", "version", "help", "events", "test", "system", "monitor", "cache", "event", "config", "whisper", "discord"}
					for _, ig := range ignoredCmds {
						if cmd == ig {
							isCognitive = false
							break
						}
					}
				}
			}

			// 3. Update Redis if Cognitive
			if isCognitive {
				// Calculate and accumulate idle time before resetting the timer
				now := time.Now().Unix()
				oldLastEventTS, err := redisClient.Get(ctx, "system:last_cognitive_event").Int64()
				if err == nil && oldLastEventTS > 0 {
					idleDuration := now - oldLastEventTS
					if idleDuration > 0 {
						redisClient.IncrBy(ctx, "system:metrics:total_idle_seconds", idleDuration)
					}
				}
				redisClient.Set(ctx, "system:last_cognitive_event", now, utils.DefaultTTL)
			}

			// Always update the raw last event timestamp for low-level debugging
			redisClient.Set(ctx, "system:last_event_ts", time.Now().Unix(), utils.DefaultTTL)
		}()

		// TRIGGER: CLI Status -> Discord
		// If we receive a CLI status update, immediately register it as a process and push it to Discord
		if eventType == string(types.EventTypeCLIStatus) {
			status, _ := eventData["status"].(string)
			message, _ := eventData["message"].(string)
			if status != "" && message != "" {
				go func(s, m string) {
					// Register as a short-lived process (1 minute TTL simulation via Del)
					// We use a specific ID for CLI operations
					processID := "system-cli-op"

					// We need a Discord client here. Since this is an endpoint,
					// we'll create a temporary one or ideally use a shared one.
					// For now, we'll use the existing logic but wrapped in ReportProcess if possible.

					discordSvcURL := "http://127.0.0.1:8300"
					dClient := discord.NewClient(discordSvcURL, "")

					// Map CLI status to machine outcome
					outcome := "unknown"
					switch s {
					case "success":
						outcome = "success"
					case "failure", "error":
						outcome = "waste"
					}

					if outcome != "unknown" {
						utils.RecordProcessOutcome(ctx, redisClient, processID, outcome)
					}

					utils.ReportProcess(ctx, redisClient, dClient, processID, m)

					// CLI statuses are usually self-completing or updated by the next event.
					// For terminal statuses, we clear the process.
					lowerM := strings.ToLower(m)
					if s == "online" || s == "success" || s == "failure" || s == "error" ||
						strings.Contains(lowerM, "complete") || strings.Contains(lowerM, "success") ||
						strings.Contains(lowerM, "failed") || strings.Contains(lowerM, "error") {
						time.Sleep(5 * time.Second) // Give user time to see the final state
						utils.ClearProcess(ctx, redisClient, dClient, processID)
					}
				}(status, message)
			}
		}

		// Validate event against template
		validationErrors := templates.Validate(eventType, eventData)
		if len(validationErrors) > 0 {
			// Build error message
			errorMsg := "Event validation failed:\n"
			for _, err := range validationErrors {
				errorMsg += fmt.Sprintf("  - %s\n", err.Error())
			}
			http.Error(w, errorMsg, http.StatusBadRequest)
			return
		}

		// Generate unique ID
		eventID := uuid.New().String()
		now := time.Now()
		timestamp := now.Unix()
		// Use microsecond precision for the score to ensure correct ordering of events within the same second
		score := float64(now.UnixMicro()) / 1000000.0

		// Create event object
		event := types.Event{
			ID:        eventID,
			Service:   req.Service,
			Event:     req.Event,
			Timestamp: timestamp,
		}

		// Marshal event to JSON
		eventJSON, err := json.Marshal(event)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to marshal event: %v", err), http.StatusInternalServerError)
			return
		}

		// Use a Redis pipeline for atomic operations
		pipe := redisClient.Pipeline()

		// Store event data in a hash
		eventKey := eventKeyPrefix + eventID
		pipe.Set(ctx, eventKey, eventJSON, utils.DefaultTTL)

		// Add event ID to the global sorted set (timeline) with timestamp as score
		pipe.ZAdd(ctx, timelineKey, redis.Z{
			Score:  score,
			Member: eventID,
		})

		// Add event ID to a service-specific sorted set for filtering
		serviceTimelineKey := fmt.Sprintf("events:service:%s", req.Service)
		pipe.ZAdd(ctx, serviceTimelineKey, redis.Z{
			Score:  score,
			Member: eventID,
		})

		// Add event ID to a type-specific sorted set for fast filtering
		typeTimelineKey := fmt.Sprintf("events:type:%s", eventType)
		pipe.ZAdd(ctx, typeTimelineKey, redis.Z{
			Score:  score,
			Member: eventID,
		})

		// Add event ID to channel-specific sorted set if channel_id or target_channel is present
		var channelID string
		if cid, ok := eventData["channel_id"].(string); ok {
			channelID = cid
		} else if tid, ok := eventData["target_channel"].(string); ok {
			channelID = tid
		}

		if channelID != "" {
			channelTimelineKey := fmt.Sprintf("events:channel:%s", channelID)
			pipe.ZAdd(ctx, channelTimelineKey, redis.Z{
				Score:  score,
				Member: eventID,
			})
		}

		// Execute pipeline
		if _, err := pipe.Exec(ctx); err != nil {
			http.Error(w, fmt.Sprintf("Failed to store event: %v", err), http.StatusInternalServerError)
			return
		}

		// Find handlers to execute
		var handlersToExecute []types.HandlerConfig

		// 1. If a specific handler is requested by `req.Handler`
		if req.Handler != "" {
			config, exists := handlers.GetHandler(req.Handler)
			if !exists {
				http.Error(w, fmt.Sprintf("Handler '%s' not found", req.Handler), http.StatusBadRequest)
				return
			}
			// Apply filter for the explicitly requested handler
			if len(config.Filters) > 0 {
				if !matchesEventFilters(req.Event, config.Filters) {
					log.Printf("Requested handler '%s' did not match event filters for event %s. No action taken.", req.Handler, eventID)
					// Still return success, event was created.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(types.CreateEventResponse{
						ID:       eventID,
						ChildIDs: []string{},
					})
					return
				}
			}
			handlersToExecute = append(handlersToExecute, *config)
		} else {
			// 2. If no specific handler is requested, find all default handlers for the event type
			potentialHandlers := handlers.GetHandlersForEventType(eventType)
			for _, h := range potentialHandlers {
				if len(h.Filters) > 0 {
					if matchesEventFilters(req.Event, h.Filters) {
						handlersToExecute = append(handlersToExecute, h)
					}
				} else {
					// No filters defined, always matches
					handlersToExecute = append(handlersToExecute, h)
				}
			}
		}

		// Execute all identified handlers
		var allChildIDs []string
		isSyncMode := req.HandlerMode == "sync" // Determine sync vs async once

		for _, hConfig := range handlersToExecute {
			currentChildIDs, err := handlers.ExecuteHandler(redisClient, &event, &hConfig, isSyncMode)
			if err != nil {
				// Log error but don't fail the overall request.
				log.Printf("Error executing handler '%s' for event %s: %v", hConfig.Name, eventID, err)
			}
			allChildIDs = append(allChildIDs, currentChildIDs...)
		}

		// Return response with the event ID and all child IDs
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(types.CreateEventResponse{
			ID:       eventID,
			ChildIDs: allChildIDs,
		})
	}
}

// GetEventByIDHandler retrieves a single event by its ID
func GetEventByIDHandler(redisClient *redis.Client, eventID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()

		// Get event from Redis
		eventKey := eventKeyPrefix + eventID
		eventJSON, err := redisClient.Get(ctx, eventKey).Result()
		if err == redis.Nil {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, fmt.Sprintf("Failed to retrieve event: %v", err), http.StatusInternalServerError)
			return
		}

		// Parse event JSON
		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse event: %v", err), http.StatusInternalServerError)
			return
		}

		// Return event
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(event)
	}
}

// GetTimelineHandler retrieves events from the timeline with filtering
func GetTimelineHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()

		// Parse query parameters
		query := r.URL.Query()

		// max_length or ml is required
		maxLengthStr := query.Get("max_length")
		if maxLengthStr == "" {
			maxLengthStr = query.Get("ml")
		}
		if maxLengthStr == "" {
			http.Error(w, "max_length (or ml) parameter is required", http.StatusBadRequest)
			return
		}

		maxLength, err := strconv.Atoi(maxLengthStr)
		if err != nil || maxLength <= 0 {
			http.Error(w, "max_length must be a positive integer", http.StatusBadRequest)
			return
		}

		// Cap max_length to prevent abuse
		if maxLength > 10000 {
			maxLength = 10000
		}

		// Parse optional min_timestamp
		var minTimestamp int64 = 0
		if minStr := query.Get("min_timestamp"); minStr != "" {
			minTimestamp, err = strconv.ParseInt(minStr, 10, 64)
			if err != nil {
				http.Error(w, "min_timestamp must be a valid unix timestamp", http.StatusBadRequest)
				return
			}
		}

		// Parse optional max_timestamp
		maxTimestamp := time.Now().Unix() + 86400 // Default to tomorrow
		if maxStr := query.Get("max_timestamp"); maxStr != "" {
			maxTimestamp, err = strconv.ParseInt(maxStr, 10, 64)
			if err != nil {
				http.Error(w, "max_timestamp must be a valid unix timestamp", http.StatusBadRequest)
				return
			}
		}

		// Parse order (default is descending/last-to-first)
		ascending := false
		if order := query.Get("order"); order == "asc" || order == "ascending" {
			ascending = true
		}

		// Parse optional service filter
		serviceFilter := query.Get("service")

		// Parse optional channel filter
		channelFilter := query.Get("channel")
		if channelFilter == "" {
			channelFilter = query.Get("channel_id")
		}

		// Parse optional type filter
		typeFilter := query.Get("type")
		if typeFilter == "" {
			typeFilter = query.Get("event.type")
		}

		// Parse optional category filter
		category := query.Get("category")
		if category != "" {
			// Define categories matching frontend and BulkDeleteEventHandler
			categories := map[string][]string{
				"messaging": {
					"message_received", "messaging.user.sent_message", "messaging.bot.sent_message",
					"messaging.user.transcribed", "voice_transcribed", "bot_response",
					"messaging.user.joined_voice", "messaging.user.left_voice",
					"messaging.bot.joined_voice", "messaging.bot.voice_response",
					"messaging.user.speaking.started", "messaging.user.speaking.stopped",
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
					"analysis.link.completed", "analysis.visual.completed",
				},
				"moderation": {
					"moderation.explicit_content.deleted",
				},
			}

			if typesList, ok := categories[category]; ok {
				// Convert to comma-separated string for multi-type support
				if typeFilter == "" {
					typeFilter = strings.Join(typesList, ",")
				} else {
					typeFilter += "," + strings.Join(typesList, ",")
				}
			}
		}

		// Parse event field filters (any query param starting with "event.")
		eventFilters := make(map[string]string)
		for key, values := range query {
			if strings.HasPrefix(key, "event.") && len(values) > 0 {
				fieldName := strings.TrimPrefix(key, "event.")
				// We already handled event.type via typeFilter if it was a single simple type
				if fieldName == "type" && !strings.Contains(values[0], ",") {
					continue
				}
				eventFilters[fieldName] = values[0]
			}
		}

		// Parse optional exclude_types filter
		excludeTypes := query.Get("exclude_types")
		excludedTypesMap := make(map[string]bool)
		if excludeTypes != "" {
			for _, t := range strings.Split(excludeTypes, ",") {
				excludedTypesMap[strings.TrimSpace(t)] = true
			}
		}

		// Determine which timeline key to use
		var queryKey string
		if typeFilter != "" && !strings.Contains(typeFilter, ",") {
			// Optimized path: query specific type directly
			queryKey = fmt.Sprintf("events:type:%s", typeFilter)
		} else if channelFilter != "" {
			// Use channel-specific timeline
			queryKey = fmt.Sprintf("events:channel:%s", channelFilter)
		} else if serviceFilter != "" {
			// Use service-specific timeline
			queryKey = fmt.Sprintf("events:service:%s", serviceFilter)
		} else {
			// Use global timeline
			queryKey = timelineKey
		}

		// Query Redis sorted set
		var eventIDs []string
		if ascending {
			// First to last (ascending by timestamp)
			eventIDs, err = redisClient.ZRangeByScore(ctx, queryKey, &redis.ZRangeBy{
				Min:   fmt.Sprintf("%d", minTimestamp),
				Max:   fmt.Sprintf("%d", maxTimestamp),
				Count: int64(maxLength),
			}).Result()
		} else {
			// Last to first (descending by timestamp)
			eventIDs, err = redisClient.ZRevRangeByScore(ctx, queryKey, &redis.ZRangeBy{
				Min:   fmt.Sprintf("%d", minTimestamp),
				Max:   fmt.Sprintf("%d", maxTimestamp),
				Count: int64(maxLength),
			}).Result()
		}

		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to retrieve timeline: %v", err), http.StatusInternalServerError)
			return
		}

		// Prepare type filter map for fast lookup in loop
		targetTypesMap := make(map[string]bool)
		if typeFilter != "" && strings.Contains(typeFilter, ",") {
			for _, t := range strings.Split(typeFilter, ",") {
				targetTypesMap[strings.TrimSpace(t)] = true
			}
		}

		// Retrieve all events from Redis and apply event field filters
		events := make([]types.Event, 0, len(eventIDs))
		for _, eventID := range eventIDs {
			eventKey := eventKeyPrefix + eventID
			eventJSON, err := redisClient.Get(ctx, eventKey).Result()
			if err == redis.Nil {
				// Event was deleted or doesn't exist, skip it
				continue
			} else if err != nil {
				http.Error(w, fmt.Sprintf("Failed to retrieve event: %v", err), http.StatusInternalServerError)
				return
			}

			var event types.Event
			if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
				// Skip malformed events
				continue
			}

			var eventData map[string]interface{}
			if err := json.Unmarshal(event.Event, &eventData); err != nil {
				continue
			}

			eventType, _ := eventData["type"].(string)

			// Apply type filter if we have multiple target types
			if len(targetTypesMap) > 0 {
				if !targetTypesMap[eventType] {
					continue
				}
			}

			// Check for excluded types
			if len(excludedTypesMap) > 0 {
				if excludedTypesMap[eventType] {
					continue
				}
			}

			// Apply event field filters if any are specified
			if len(eventFilters) > 0 {
				if !matchesEventFilters(event.Event, eventFilters) {
					continue // Skip events that don't match filters
				}
			}

			events = append(events, event)
		}

		// Check format parameter
		format := query.Get("format")
		if format == "text" {
			// If we fetched descending (newest first) to get the latest N items,
			// we typically want to read them chronologically (oldest first) in a text log.
			if !ascending {
				for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
					events[i], events[j] = events[j], events[i]
				}
			}

			// Render as human-readable text
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)

			// Get optional timezone and language parameters
			timezone := query.Get("timezone")
			languageParam := query.Get("lang")

			// Resolve language to ISO code (supports codes, English names, and native names)
			language := templates.ResolveLanguage(languageParam)

			// Build parent-child depth map
			depthMap := buildDepthMap(events)

			// Format each event
			for _, event := range events {
				// Parse event data to get type
				var eventData map[string]interface{}
				if err := json.Unmarshal(event.Event, &eventData); err != nil {
					continue
				}

				eventType, _ := eventData["type"].(string)
				depth := depthMap[event.ID]

				// Format and write line
				line := templates.FormatEventAsText(eventType, eventData, event.Service, event.Timestamp, depth, timezone, language)
				_, _ = fmt.Fprintln(w, line) // Ignore write errors to HTTP response
			}
			return
		}

		// Default: Return JSON response
		response := types.GetTimelineResponse{
			Events: events,
			Count:  len(events),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}
}

// buildDepthMap calculates the depth of each event in the parent-child tree
func buildDepthMap(events []types.Event) map[string]int {
	depthMap := make(map[string]int)

	// First pass: identify all root events (no parent)
	for _, event := range events {
		if event.ParentID == "" {
			depthMap[event.ID] = 0
		}
	}

	// Iteratively calculate depths for children
	changed := true
	for changed {
		changed = false
		for _, event := range events {
			if event.ParentID != "" {
				if parentDepth, exists := depthMap[event.ParentID]; exists {
					childDepth := parentDepth + 1
					if currentDepth, hasDepth := depthMap[event.ID]; !hasDepth || currentDepth != childDepth {
						depthMap[event.ID] = childDepth
						changed = true
					}
				}
			}
		}
	}

	return depthMap
}

// DeleteEventHandler deletes an event following parent-child rules
func DeleteEventHandler(redisClient *redis.Client, eventID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()

		// Get event from Redis
		eventKey := eventKeyPrefix + eventID
		eventJSON, err := redisClient.Get(ctx, eventKey).Result()
		if err == redis.Nil {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, fmt.Sprintf("Failed to retrieve event: %v", err), http.StatusInternalServerError)
			return
		}

		// Parse event
		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse event: %v", err), http.StatusInternalServerError)
			return
		}

		// Check if this is a child event
		if event.ParentID != "" {
			// Verify this child can be deleted
			// Rule: Can only delete if it's the last child and has no descendants
			if len(event.ChildIDs) > 0 {
				http.Error(w, "Cannot delete child event: it has spawned descendants", http.StatusForbidden)
				return
			}

			// Get parent event to check if this is the last child
			parentKey := eventKeyPrefix + event.ParentID
			parentJSON, err := redisClient.Get(ctx, parentKey).Result()
			if err == nil {
				var parent types.Event
				if err := json.Unmarshal([]byte(parentJSON), &parent); err == nil {
					// Check if this is the last child
					if len(parent.ChildIDs) > 0 {
						lastChildID := parent.ChildIDs[len(parent.ChildIDs)-1]
						if lastChildID != eventID {
							http.Error(w, "Cannot delete child event: only the last child can be deleted", http.StatusForbidden)
							return
						}
					}
				}
			}
		}

		// If this event has children, delete them all (cascade delete)
		if len(event.ChildIDs) > 0 {
			for _, childID := range event.ChildIDs {
				if err := deleteEventByID(redisClient, ctx, childID); err != nil {
					http.Error(w, fmt.Sprintf("Failed to delete child event %s: %v", childID, err), http.StatusInternalServerError)
					return
				}
			}
		}

		// Delete the event itself
		if err := deleteEventByID(redisClient, ctx, eventID); err != nil {
			http.Error(w, fmt.Sprintf("Failed to delete event: %v", err), http.StatusInternalServerError)
			return
		}

		// If this was a child, update parent's child_ids list
		if event.ParentID != "" {
			if err := removeChildFromParent(redisClient, ctx, event.ParentID, eventID); err != nil {
				// Log error but don't fail the request - the event is already deleted
				fmt.Printf("Warning: Failed to update parent event: %v\n", err)
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// deleteEventByID deletes an event and removes it from all timelines
func deleteEventByID(redisClient *redis.Client, ctx context.Context, eventID string) error {
	// Get event to find service
	eventKey := eventKeyPrefix + eventID
	eventJSON, err := redisClient.Get(ctx, eventKey).Result()
	if err != nil {
		return err
	}

	var event types.Event
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		return err
	}

	// Use pipeline for atomic deletion
	pipe := redisClient.Pipeline()

	// Delete event data
	pipe.Del(ctx, eventKey)

	// Remove from global timeline
	pipe.ZRem(ctx, timelineKey, eventID)

	// Remove from service timeline
	serviceTimelineKey := fmt.Sprintf("events:service:%s", event.Service)
	pipe.ZRem(ctx, serviceTimelineKey, eventID)

	// Remove from type timeline
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err == nil {
		if eventType, ok := eventData["type"].(string); ok {
			typeTimelineKey := fmt.Sprintf("events:type:%s", eventType)
			pipe.ZRem(ctx, typeTimelineKey, eventID)
		}

		// Remove from channel timeline if applicable
		var channelID string
		if cid, ok := eventData["channel_id"].(string); ok {
			channelID = cid
		} else if tid, ok := eventData["target_channel"].(string); ok {
			channelID = tid
		}

		if channelID != "" {
			channelTimelineKey := fmt.Sprintf("events:channel:%s", channelID)
			pipe.ZRem(ctx, channelTimelineKey, eventID)
		}
	}

	// Execute pipeline
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	return nil
}

// removeChildFromParent removes a child ID from parent's child_ids array
func removeChildFromParent(redisClient *redis.Client, ctx context.Context, parentID string, childID string) error {
	// Get parent event
	parentKey := eventKeyPrefix + parentID
	parentJSON, err := redisClient.Get(ctx, parentKey).Result()
	if err != nil {
		return err
	}

	var parent types.Event
	if err := json.Unmarshal([]byte(parentJSON), &parent); err != nil {
		return err
	}

	// Remove child from list
	newChildIDs := []string{}
	for _, id := range parent.ChildIDs {
		if id != childID {
			newChildIDs = append(newChildIDs, id)
		}
	}
	parent.ChildIDs = newChildIDs

	// Update parent
	updatedJSON, err := json.Marshal(parent)
	if err != nil {
		return err
	}

	if err := redisClient.Set(ctx, parentKey, updatedJSON, utils.DefaultTTL).Err(); err != nil {
		return err
	}

	return nil
}

// PatchEventHandler updates specific fields in an event's JSON data
func PatchEventHandler(redisClient *redis.Client, eventID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()

		// Get event from Redis
		eventKey := eventKeyPrefix + eventID
		eventJSON, err := redisClient.Get(ctx, eventKey).Result()
		if err == redis.Nil {
			http.Error(w, "Event not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, fmt.Sprintf("Failed to retrieve event: %v", err), http.StatusInternalServerError)
			return
		}

		// Parse event
		var event types.Event
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse event: %v", err), http.StatusInternalServerError)
			return
		}

		// Parse patch data from request body
		var patchData map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&patchData); err != nil {
			http.Error(w, fmt.Sprintf("Invalid patch data: %v", err), http.StatusBadRequest)
			return
		}

		// Parse existing event data
		var eventData map[string]interface{}
		if err := json.Unmarshal(event.Event, &eventData); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse event data: %v", err), http.StatusInternalServerError)
			return
		}

		// Apply patch
		for k, v := range patchData {
			eventData[k] = v
		}

		// Marshal back to JSON
		updatedEventData, err := json.Marshal(eventData)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to marshal updated event data: %v", err), http.StatusInternalServerError)
			return
		}

		event.Event = updatedEventData

		// Marshal full event
		updatedEventJSON, err := json.Marshal(event)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to marshal updated event: %v", err), http.StatusInternalServerError)
			return
		}

		// Save back to Redis
		if err := redisClient.Set(ctx, eventKey, updatedEventJSON, utils.DefaultTTL).Err(); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save updated event: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(event)
	}
}
