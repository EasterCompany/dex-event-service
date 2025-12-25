package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	internalHandlers "github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/internal/handlers/analyst"
	"github.com/EasterCompany/dex-event-service/internal/handlers/greeting"
	"github.com/EasterCompany/dex-event-service/internal/handlers/privatemessage"
	"github.com/EasterCompany/dex-event-service/internal/handlers/publicmessage"
	"github.com/EasterCompany/dex-event-service/internal/handlers/transcription"
	"github.com/EasterCompany/dex-event-service/internal/handlers/webhook"
)

const (
	defaultTimeout = 3600 // seconds (1 hour)
	eventKeyPrefix = "event:"
	timelineKey    = "events:timeline"
)

var (
	internalRegistry = map[string]internalHandlers.HandlerFunc{
		"public-message-handler":  publicmessage.Handle,
		"private-message-handler": privatemessage.Handle,
		"greeting-handler":        greeting.Handle,
		"transcription-handler":   transcription.Handle,
		"webhook-handler":         webhook.Handle,
		// "test": test.Handle, // If we implemented test
	}
	dependencies    *internalHandlers.Dependencies
	jobQueue        chan *job
	workerOnce      sync.Once
	interruptionMap sync.Map // Map[channelID]int64 (timestamp)

	// runningBackgroundHandlers stores references to initialized background workers
	runningBackgroundHandlers = make(map[string]internalHandlers.BackgroundHandler)
	muBackgroundHandlers      sync.Mutex // Protects runningBackgroundHandlers
)

type job struct {
	Event      *types.Event
	Config     *types.HandlerConfig
	ResultChan chan jobResult
}

type jobResult struct {
	ChildIDs []string
	Error    error
}

// InitExecutor initializes the executor with dependencies and starts the worker.
func InitExecutor(deps *internalHandlers.Dependencies) {
	dependencies = deps
	jobQueue = make(chan *job, 1000) // Buffer size
	workerOnce.Do(func() {
		go startWorker()
	})
	log.Println("Executor initialized with single-worker queue.")

	// Initialize and run background handlers
	initBackgroundHandlers()
}

func initBackgroundHandlers() {
	muBackgroundHandlers.Lock()
	defer muBackgroundHandlers.Unlock()

	for _, handlerConfig := range handlerConfigs {
		if handlerConfig.IsBackgroundWorker {
			switch handlerConfig.Name {
			case analyst.HandlerName:
				analystHandler := analyst.NewAnalystHandler(dependencies.Redis, dependencies.Ollama, dependencies.Discord, dependencies.Web, dependencies.Options)
				if err := analystHandler.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize analyst handler: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = analystHandler
				log.Printf("Background handler '%s' started.", handlerConfig.Name)
			default:
				log.Printf("WARNING: Unknown background handler '%s'. Not started.", handlerConfig.Name)
			}
		}
	}
}

// CloseExecutor stops all running background handlers.
func CloseExecutor() {
	muBackgroundHandlers.Lock()
	defer muBackgroundHandlers.Unlock()

	for name, handler := range runningBackgroundHandlers {
		log.Printf("Closing background handler '%s'...", name)
		if err := handler.Close(); err != nil {
			log.Printf("ERROR: Failed to close background handler '%s': %v", name, err)
		} else {
			log.Printf("Background handler '%s' closed.", name)
		}
	}
	runningBackgroundHandlers = make(map[string]internalHandlers.BackgroundHandler) // Clear the map
}

func startWorker() {
	for j := range jobQueue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("CRITICAL: Worker panicked while processing event %s: %v", j.Event.ID, r)
					// Optionally create an error event here if possible
					if j.ResultChan != nil {
						j.ResultChan <- jobResult{Error: fmt.Errorf("worker panic: %v", r)}
					}
				}
			}()
			childIDs, err := executeHandlerInternal(j.Event, j.Config)
			if j.ResultChan != nil {
				j.ResultChan <- jobResult{ChildIDs: childIDs, Error: err}
			}
		}()
	}
}

// ExecuteHandler queues a handler for execution (sync or async)
func ExecuteHandler(
	redisClient *redis.Client,
	event *types.Event,
	handlerConfig *types.HandlerConfig,
	isSync bool,
) ([]string, error) {
	if dependencies == nil {
		return nil, fmt.Errorf("executor not initialized")
	}

	// Update interruption map if channel_id is present
	// We parse lightly to avoid heavy overhead, or just unmarshal as map
	var evtData map[string]interface{}
	if err := json.Unmarshal(event.Event, &evtData); err == nil {
		if cid, ok := evtData["channel_id"].(string); ok && cid != "" {
			interruptionMap.Store(cid, event.Timestamp)
		}
	}

	// If sync, create a channel to wait for result
	if isSync {
		resChan := make(chan jobResult)
		jobQueue <- &job{
			Event:      event,
			Config:     handlerConfig,
			ResultChan: resChan,
		}
		res := <-resChan
		return res.ChildIDs, res.Error
	}

	// Async: Fire and forget
	select {
	case jobQueue <- &job{
		Event:  event,
		Config: handlerConfig,
	}:
		// Queued successfully
	default:
		log.Printf("Warning: Job queue full, dropping event %s for handler %s", event.ID, handlerConfig.Name)
		return nil, fmt.Errorf("job queue full")
	}

	return []string{}, nil
}

// executeHandlerInternal is the actual logic running in the worker
func executeHandlerInternal(
	event *types.Event,
	handlerConfig *types.HandlerConfig,
) ([]string, error) {
	ctx := context.Background()
	// Redis client from dependencies
	redisClient := dependencies.Redis

	// Parse event data to get event type
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err != nil {
		return nil, fmt.Errorf("failed to parse event data: %v", err)
	}

	eventType, _ := eventData["type"].(string)
	channelID, _ := eventData["channel_id"].(string)

	// Build handler input
	input := types.HandlerInput{
		EventID:   event.ID,
		Service:   event.Service,
		EventType: eventType,
		EventData: eventData,
		Timestamp: event.Timestamp,
	}

	// Set timeout
	timeout := handlerConfig.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	log.Printf("Executing handler '%s' for event %s", handlerConfig.Name, event.ID)

	var output types.HandlerOutput
	var handlerErr error

	// Create local dependencies with Interruption Checker
	localDeps := *dependencies
	localDeps.CheckInterruption = func() bool {
		if channelID == "" {
			return false
		}
		if val, ok := interruptionMap.Load(channelID); ok {
			latestTime := val.(int64)
			// If a newer event exists (latestTime > event.Timestamp), we are interrupted.
			if latestTime > event.Timestamp {
				log.Printf("Handler INTERRUPTED: Channel %s has newer event (Time %d > %d)", channelID, latestTime, event.Timestamp)
				return true
			}
		}
		return false
	}

	// Check if internal handler exists
	if handlerFunc, ok := internalRegistry[handlerConfig.Name]; ok {
		output, handlerErr = handlerFunc(execCtx, input, &localDeps)
	} else {
		// Fallback or Error?
		// For now, let's treat "test" handler specially or return error
		if handlerConfig.Name == "test" {
			// Simple test echo
			output = types.HandlerOutput{Success: true}
		} else {
			handlerErr = fmt.Errorf("handler '%s' not implemented internally", handlerConfig.Name)
		}
	}

	if execCtx.Err() == context.DeadlineExceeded {
		log.Printf("Handler '%s' timed out after %d seconds", handlerConfig.Name, timeout)
		return createErrorEvent(redisClient, event, handlerConfig.Name, fmt.Sprintf("Handler '%s' timed out after %d seconds", handlerConfig.Name, timeout))
	}

	if handlerErr != nil {
		log.Printf("Handler '%s' failed: %v", handlerConfig.Name, handlerErr)
		return createErrorEvent(redisClient, event, handlerConfig.Name, handlerErr.Error())
	}

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

	// log.Printf("Handler '%s' completed in %v, created %d child events", handlerConfig.Name, duration, len(childIDs))
	return childIDs, nil
}

// createChildEvent creates a child event in Redis
func createChildEvent(redisClient *redis.Client, parent *types.Event, childEventData types.HandlerOutputEvent) (string, error) {
	ctx := context.Background()

	validationErrors := templates.Validate(childEventData.Type, childEventData.Data)
	if len(validationErrors) > 0 {
		errMsg := "Child event validation failed:\n"
		for _, verr := range validationErrors {
			errMsg += fmt.Sprintf("  - %s\n", verr.Error())
		}
		return "", fmt.Errorf("%s", errMsg)
	}

	childID := uuid.New().String()
	timestamp := time.Now().Unix()

	childEventData.Data["type"] = childEventData.Type

	eventJSON, err := json.Marshal(childEventData.Data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal child event data: %v", err)
	}

	childEvent := types.Event{
		ID:        childID,
		Service:   parent.Service,
		Event:     eventJSON,
		Timestamp: timestamp,
		ParentID:  parent.ID,
		ChildIDs:  []string{},
	}

	childEventJSON, err := json.Marshal(childEvent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal child event: %v", err)
	}

	pipe := redisClient.Pipeline()
	eventKey := eventKeyPrefix + childID
	pipe.Set(ctx, eventKey, childEventJSON, 0)
	pipe.ZAdd(ctx, timelineKey, redis.Z{Score: float64(timestamp), Member: childID})
	serviceTimelineKey := fmt.Sprintf("events:service:%s", parent.Service)
	pipe.ZAdd(ctx, serviceTimelineKey, redis.Z{Score: float64(timestamp), Member: childID})

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
