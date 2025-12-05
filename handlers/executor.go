package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	defaultTimeout = 30 // seconds
	eventKeyPrefix = "event:"
	timelineKey    = "events:timeline"
)

type runningTask struct {
	id     string
	cancel context.CancelFunc
}

var (
	runningTasks = make(map[string]runningTask)
	taskMutex    sync.Mutex
)

// ExecuteHandler runs a handler for an event (sync or async)
// Returns child event IDs (for sync) or empty slice (for async)
func ExecuteHandler(
	redisClient *redis.Client,
	event *types.Event,
	handlerConfig *types.HandlerConfig,
	isSync bool,
) ([]string, error) {
	if isSync {
		// Synchronous: execute and wait
		return executeHandlerSync(redisClient, event, handlerConfig)
	}

	// Asynchronous: spawn goroutine and return immediately
	go func() {
		_, err := executeHandlerSync(redisClient, event, handlerConfig)
		if err != nil {
			log.Printf("Async handler '%s' error: %v", handlerConfig.Name, err)
		}
	}()

	return []string{}, nil
}

// executeHandlerSync runs the handler and waits for completion
func executeHandlerSync(
	redisClient *redis.Client,
	event *types.Event,
	handlerConfig *types.HandlerConfig,
) ([]string, error) {
	ctx := context.Background()

	// Parse event data to get event type
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err != nil {
		return nil, fmt.Errorf("failed to parse event data: %v", err)
	}

	eventType, _ := eventData["type"].(string)

	// Build handler input
	input := types.HandlerInput{
		EventID:   event.ID,
		Service:   event.Service,
		EventType: eventType,
		EventData: eventData,
		Timestamp: event.Timestamp,
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal handler input: %v", err)
	}

	// Get binary path
	binaryPath := GetBinaryPath(handlerConfig.Binary)

	// Set timeout
	timeout := handlerConfig.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Check for debounce key
	if handlerConfig.DebounceKey != "" {
		keyVal := getDebounceKey(eventData, handlerConfig.DebounceKey)
		if keyVal != "" {
			// Construct unique key for this handler + value
			taskKey := fmt.Sprintf("%s:%s", handlerConfig.Name, keyVal)

			taskMutex.Lock()
			if existing, ok := runningTasks[taskKey]; ok {
				log.Printf("Debounce: Cancelling existing task for key %s (Event ID: %s)", taskKey, existing.id)
				existing.cancel()
			}
			runningTasks[taskKey] = runningTask{
				id:     event.ID,
				cancel: cancel,
			}
			taskMutex.Unlock()

			// Cleanup on completion
			defer func() {
				taskMutex.Lock()
				// Only remove if it's still this specific task (by ID)
				if existing, ok := runningTasks[taskKey]; ok && existing.id == event.ID {
					delete(runningTasks, taskKey)
				}
				taskMutex.Unlock()
			}()
		}
	}

	// Execute handler
	cmd := exec.CommandContext(execCtx, binaryPath)
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("Executing handler '%s' for event %s", handlerConfig.Name, event.ID)

	startTime := time.Now()
	execErr := cmd.Run()
	duration := time.Since(startTime)

	// Check for timeout
	if execCtx.Err() == context.DeadlineExceeded {
		log.Printf("Handler '%s' timed out after %d seconds", handlerConfig.Name, timeout)
		return createErrorEvent(redisClient, event, handlerConfig.Name, fmt.Sprintf("Handler '%s' timed out after %d seconds", handlerConfig.Name, timeout))
	}

	// Check for execution error
	if execErr != nil {
		errMsg := fmt.Sprintf("Handler execution failed: %v", execErr)
		if stderr.Len() > 0 {
			errMsg += fmt.Sprintf("\nStderr: %s", stderr.String())
		}
		log.Printf("Handler '%s' failed: %s", handlerConfig.Name, errMsg)
		return createErrorEvent(redisClient, event, handlerConfig.Name, errMsg)
	}

	// Parse handler output
	var output types.HandlerOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		errMsg := fmt.Sprintf("Failed to parse handler output: %v\nOutput: %s", err, stdout.String())
		log.Printf("Handler '%s' produced invalid output: %s", handlerConfig.Name, errMsg)
		return createErrorEvent(redisClient, event, handlerConfig.Name, errMsg)
	}

	// Check if handler reported failure
	if !output.Success {
		errMsg := output.Error
		if errMsg == "" {
			errMsg = "Handler reported failure with no error message"
		}
		log.Printf("Handler '%s' reported failure: %s", handlerConfig.Name, errMsg)
		return createErrorEvent(redisClient, event, handlerConfig.Name, errMsg)
	}

	// Create child events
	var childIDs []string
	for i, childEvent := range output.Events {
		childID, err := createChildEvent(redisClient, event, childEvent)
		if err != nil {
			log.Printf("Failed to create child event %d from handler '%s': %v", i, handlerConfig.Name, err)
			// Create error event for this failure
			errorIDs, _ := createErrorEvent(redisClient, event, handlerConfig.Name, fmt.Sprintf("Failed to create child event: %v", err))
			childIDs = append(childIDs, errorIDs...)
			continue
		}
		childIDs = append(childIDs, childID)
	}

	// Update parent event with child IDs
	if len(childIDs) > 0 {
		event.ChildIDs = childIDs
		if err := updateEvent(redisClient, event); err != nil {
			log.Printf("Failed to update parent event with child IDs: %v", err)
		}
	}

	log.Printf("Handler '%s' completed in %v, created %d child events", handlerConfig.Name, duration, len(childIDs))
	return childIDs, nil
}

// getDebounceKey extracts a value from the event data using dot notation
func getDebounceKey(eventData map[string]interface{}, keyPath string) string {
	fields := strings.Split(keyPath, ".")
	var currentValue interface{} = eventData

	for _, field := range fields {
		if currentMap, ok := currentValue.(map[string]interface{}); ok {
			currentValue = currentMap[field]
		} else {
			return ""
		}
	}
	return fmt.Sprintf("%v", currentValue)
}

// createChildEvent creates a child event in Redis
func createChildEvent(redisClient *redis.Client, parent *types.Event, childEventData types.HandlerOutputEvent) (string, error) {
	ctx := context.Background()

	// Validate child event against template
	validationErrors := templates.Validate(childEventData.Type, childEventData.Data)
	if len(validationErrors) > 0 {
		errMsg := "Child event validation failed:\n"
		for _, verr := range validationErrors {
			errMsg += fmt.Sprintf("  - %s\n", verr.Error())
		}
		return "", fmt.Errorf("%s", errMsg)
	}

	// Generate ID and timestamp
	childID := uuid.New().String()
	timestamp := time.Now().Unix()

	// Add type field to event data (required for all events)
	childEventData.Data["type"] = childEventData.Type

	// Marshal event data
	eventJSON, err := json.Marshal(childEventData.Data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal child event data: %v", err)
	}

	// Create child event
	childEvent := types.Event{
		ID:        childID,
		Service:   parent.Service,
		Event:     eventJSON,
		Timestamp: timestamp,
		ParentID:  parent.ID,
		ChildIDs:  []string{}, // Children can't have handlers, so no children
	}

	// Marshal full event
	childEventJSON, err := json.Marshal(childEvent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal child event: %v", err)
	}

	// Store in Redis
	pipe := redisClient.Pipeline()

	eventKey := eventKeyPrefix + childID
	pipe.Set(ctx, eventKey, childEventJSON, 0)

	// Add to timeline
	pipe.ZAdd(ctx, timelineKey, redis.Z{
		Score:  float64(timestamp),
		Member: childID,
	})

	// Add to service timeline
	serviceTimelineKey := fmt.Sprintf("events:service:%s", parent.Service)
	pipe.ZAdd(ctx, serviceTimelineKey, redis.Z{
		Score:  float64(timestamp),
		Member: childID,
	})

	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("failed to store child event in Redis: %v", err)
	}

	return childID, nil
}

// createErrorEvent creates an error_occurred event as a child
func createErrorEvent(redisClient *redis.Client, parent *types.Event, handlerName, errorMsg string) ([]string, error) {
	errorEventData := types.HandlerOutputEvent{
		Type: "error_occurred",
		Data: map[string]interface{}{
			"error":      errorMsg,
			"error_type": "HandlerError",
			"severity":   "high",
			"context": map[string]interface{}{
				"handler":        handlerName,
				"parent_event":   parent.ID,
				"parent_service": parent.Service,
			},
		},
	}

	childID, err := createChildEvent(redisClient, parent, errorEventData)
	if err != nil {
		return nil, err
	}

	// Update parent event with error child ID
	parent.ChildIDs = []string{childID}
	if err := updateEvent(redisClient, parent); err != nil {
		log.Printf("Failed to update parent event with error child ID: %v", err)
	}

	return []string{childID}, nil
}

// updateEvent updates an existing event in Redis
func updateEvent(redisClient *redis.Client, event *types.Event) error {
	ctx := context.Background()

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %v", err)
	}

	eventKey := eventKeyPrefix + event.ID
	if err := redisClient.Set(ctx, eventKey, eventJSON, 0).Err(); err != nil {
		return fmt.Errorf("failed to update event in Redis: %v", err)
	}

	return nil
}
