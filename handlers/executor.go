package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	internalHandlers "github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/internal/handlers/architect"
	"github.com/EasterCompany/dex-event-service/internal/handlers/courier"
	"github.com/EasterCompany/dex-event-service/internal/handlers/fabricator"
	"github.com/EasterCompany/dex-event-service/internal/handlers/greeting"
	"github.com/EasterCompany/dex-event-service/internal/handlers/guardian"
	"github.com/EasterCompany/dex-event-service/internal/handlers/privatemessage"
	"github.com/EasterCompany/dex-event-service/internal/handlers/profiler"
	"github.com/EasterCompany/dex-event-service/internal/handlers/publicmessage"
	"github.com/EasterCompany/dex-event-service/internal/handlers/transcription"
	"github.com/EasterCompany/dex-event-service/internal/handlers/webhook"
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
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
		"profiler-handler":        profiler.Handle,
	}
	dependencies    *internalHandlers.Dependencies
	jobQueue        chan *job
	workerOnce      sync.Once
	interruptionMap sync.Map // Map[channelID]int64 (timestamp)

	// runningBackgroundHandlers stores references to initialized background workers
	runningBackgroundHandlers = make(map[string]internalHandlers.BackgroundHandler)
	muBackgroundHandlers      sync.Mutex // Protects runningBackgroundHandlers

	// GuardianTrigger is a global hook for the API to call the background worker logic
	GuardianTrigger func(int) ([]interface{}, error)

	// AnalyzerTrigger is a global hook for the API to call the background worker logic
	AnalyzerTrigger func() error

	// FabricatorTrigger is a global hook for the API to call the background worker logic
	FabricatorTrigger func() error

	// CourierTrigger is a global hook for the API to call the background worker logic (compression)
	CourierTrigger func(bool, string) error
)

type job struct {
	Context    context.Context
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
		go startZombieReaper()
	})
	log.Println("Executor initialized with single-worker queue and Zombie Reaper.")

	// Initialize and run background handlers
	initBackgroundHandlers()
}

// startZombieReaper periodically cleans up processes belonging to dead PIDs.
func startZombieReaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if dependencies == nil || dependencies.Redis == nil {
			continue
		}

		ctx := context.Background()
		// Scan for all active process info keys
		iter := dependencies.Redis.Scan(ctx, 0, "process:info:*", 0).Iterator()

		for iter.Next(ctx) {
			key := iter.Val()
			data, err := dependencies.Redis.Get(ctx, key).Result()
			if err != nil {
				continue
			}

			var pi struct {
				PID       int    `json:"pid"`
				ChannelID string `json:"channel_id"`
				State     string `json:"state"`
			}
			if err := json.Unmarshal([]byte(data), &pi); err != nil {
				continue
			}

			// If PID is 0, we can't reliably reap it unless we add service origin tracking.
			// For now, we only reap non-zero PIDs.
			if pi.PID <= 0 {
				continue
			}

			// Check if process is alive
			process, err := os.FindProcess(pi.PID)
			if err != nil {
				log.Printf("Zombie Reaper: PID %d not found. Reaping process %s...", pi.PID, pi.ChannelID)
				reapProcess(ctx, pi.ChannelID)
				continue
			}

			// On Unix, FindProcess always succeeds. We must send signal 0 to check if it exists.
			err = process.Signal(syscall.Signal(0))
			if err != nil {
				log.Printf("Zombie Reaper: PID %d is dead. Reaping process %s...", pi.PID, pi.ChannelID)
				reapProcess(ctx, pi.ChannelID)
			}
		}
	}
}

func reapProcess(ctx context.Context, id string) {
	// Call utils logic to clear process correctly (updates busy count and status)
	utils.ClearProcess(ctx, dependencies.Redis, dependencies.Discord, id)

	// Emit unregistration event for visibility
	_, _ = utils.SendEvent(ctx, dependencies.Redis, "zombie-reaper", "system.process.reaped", map[string]interface{}{
		"process_id": id,
		"status":     "reaped",
		"reason":     "PID deceased",
		"timestamp":  time.Now().Unix(),
	})
}

func initBackgroundHandlers() {
	muBackgroundHandlers.Lock()
	defer muBackgroundHandlers.Unlock()

	for _, handlerConfig := range handlerConfigs {
		if handlerConfig.IsBackgroundWorker {
			switch handlerConfig.Name {
			case guardian.HandlerName:
				guardianHandler := guardian.NewGuardianHandler(dependencies.Redis, dependencies.Model, dependencies.Discord, dependencies.Web, dependencies.Options)
				if err := guardianHandler.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize guardian handler: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = guardianHandler

				// Wire up the trigger
				GuardianTrigger = func(tier int) ([]interface{}, error) {
					results, _, err := guardianHandler.PerformAnalysis(context.Background(), tier)
					if err != nil {
						return nil, err
					}
					// Convert results to generic interface slice for the API
					genericResults := make([]interface{}, len(results))
					for i, res := range results {
						genericResults[i] = res
					}
					return genericResults, nil
				}

				log.Printf("Background handler '%s' started.", handlerConfig.Name)
			case architect.HandlerName:
				architectHandler := architect.NewArchitectHandler(dependencies.Redis, dependencies.Model, dependencies.Discord)
				if err := architectHandler.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize architect handler: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = architectHandler
				log.Printf("Background handler '%s' started.", handlerConfig.Name)
			case profiler.AnalyzerHandlerName:
				analyzerAgent := profiler.NewAnalyzerAgent(dependencies.Redis, dependencies.Model, dependencies.Discord)
				if err := analyzerAgent.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize analyzer agent: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = analyzerAgent

				// Wire up the trigger
				AnalyzerTrigger = func() error {
					analyzerAgent.PerformSynthesis(context.Background())
					return nil
				}

				log.Printf("Background handler '%s' started.", handlerConfig.Name)
			case fabricator.HandlerName:
				fabricatorHandler := fabricator.NewFabricatorHandler(dependencies.Redis, dependencies.Model, dependencies.Discord)
				if err := fabricatorHandler.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize fabricator handler: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = fabricatorHandler

				// Wire up the trigger
				FabricatorTrigger = func() error {
					_, _, _ = fabricatorHandler.Run(context.Background())
					return nil
				}

				log.Printf("Background handler '%s' started.", handlerConfig.Name)
			case courier.HandlerName:
				courierHandler := courier.NewCourierHandler(dependencies.Redis, dependencies.Model, dependencies.Discord, dependencies.Web, dependencies.Options)
				if err := courierHandler.Init(context.Background()); err != nil {
					log.Printf("ERROR: Failed to initialize courier handler: %v", err)
					continue
				}
				runningBackgroundHandlers[handlerConfig.Name] = courierHandler

				// Wire up the trigger
				CourierTrigger = func(force bool, channelID string) error {
					if channelID != "" {
						// Single channel forced compression
						go smartcontext.UpdateSummary(context.Background(), dependencies.Redis, dependencies.Model, dependencies.Discord, channelID, courierHandler.Config.Models["compressor"], smartcontext.CachedSummary{}, nil, nil)
						return nil
					}
					return courierHandler.PerformCompression(context.Background())
				}

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
	runningBackgroundHandlers = make(map[string]internalHandlers.BackgroundHandler)
}

func startWorker() {
	for j := range jobQueue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("CRITICAL: Worker panicked while processing event %s: %v", j.Event.ID, r)
					if j.ResultChan != nil {
						j.ResultChan <- jobResult{Error: fmt.Errorf("worker panic: %v", r)}
					}
				}
			}()
			childIDs, err := executeHandlerInternal(j.Context, j.Event, j.Config)
			if j.ResultChan != nil {
				j.ResultChan <- jobResult{ChildIDs: childIDs, Error: err}
			}
		}()
	}
}

func ExecuteHandler(ctx context.Context, redisClient *redis.Client, event *types.Event, handlerConfig *types.HandlerConfig, isSync bool) ([]string, error) {
	if dependencies == nil {
		return nil, fmt.Errorf("executor not initialized")
	}
	var evtData map[string]interface{}
	if err := json.Unmarshal(event.Event, &evtData); err == nil {
		if cid, ok := evtData["channel_id"].(string); ok && cid != "" {
			interruptionMap.Store(cid, event.Timestamp)
		}
	}
	if isSync {
		resChan := make(chan jobResult)
		jobQueue <- &job{Context: ctx, Event: event, Config: handlerConfig, ResultChan: resChan}
		res := <-resChan
		return res.ChildIDs, res.Error
	}
	select {
	case jobQueue <- &job{Context: ctx, Event: event, Config: handlerConfig}:
	default:
		return nil, fmt.Errorf("job queue full")
	}
	return []string{}, nil
}

func executeHandlerInternal(ctx context.Context, event *types.Event, handlerConfig *types.HandlerConfig) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	redisClient := dependencies.Redis
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err != nil {
		return nil, fmt.Errorf("failed to parse event data: %v", err)
	}
	eventType, _ := eventData["type"].(string)
	channelID, _ := eventData["channel_id"].(string)
	input := types.HandlerInput{
		EventID: event.ID, Service: event.Service, EventType: eventType, EventData: eventData, Timestamp: event.Timestamp,
	}
	timeout := handlerConfig.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var output types.HandlerOutput
	var handlerErr error
	localDeps := *dependencies
	localDeps.CheckInterruption = func() bool {
		if channelID == "" {
			return false
		}
		if val, ok := interruptionMap.Load(channelID); ok {
			if val.(int64) > event.Timestamp {
				return true
			}
		}
		return false
	}
	if handlerFunc, ok := internalRegistry[handlerConfig.Name]; ok {
		output, handlerErr = handlerFunc(execCtx, input, &localDeps)
	} else {
		if handlerConfig.Name == "test" {
			output = types.HandlerOutput{Success: true}
		} else {
			handlerErr = fmt.Errorf("handler '%s' not implemented internally", handlerConfig.Name)
		}
	}
	if execCtx.Err() == context.DeadlineExceeded {
		return createErrorEvent(redisClient, event, handlerConfig.Name, fmt.Sprintf("Handler '%s' timed out after %d seconds", handlerConfig.Name, timeout))
	}
	if handlerErr != nil {
		return createErrorEvent(redisClient, event, handlerConfig.Name, handlerErr.Error())
	}
	if !output.Success {
		return createErrorEvent(redisClient, event, handlerConfig.Name, output.Error)
	}
	var childIDs []string
	for i, childEvent := range output.Events {
		childID, err := createChildEvent(redisClient, event, childEvent)
		if err != nil {
			log.Printf("Failed to create child event %d from handler '%s': %v", i, handlerConfig.Name, err)
			continue
		}
		childIDs = append(childIDs, childID)
	}
	if len(childIDs) > 0 {
		event.ChildIDs = childIDs
		_ = updateEvent(redisClient, event)
	}
	return childIDs, nil
}

func createChildEvent(redisClient *redis.Client, parent *types.Event, childEventData types.HandlerOutputEvent) (string, error) {
	ctx := context.Background()
	if errs := templates.Validate(childEventData.Type, childEventData.Data); len(errs) > 0 {
		return "", fmt.Errorf("validation failed")
	}
	childID := uuid.New().String()
	timestamp := time.Now().Unix()
	childEventData.Data["type"] = childEventData.Type
	eventJSON, _ := json.Marshal(childEventData.Data)
	childEvent := types.Event{
		ID: childID, Service: parent.Service, Event: eventJSON, Timestamp: timestamp, ParentID: parent.ID, ChildIDs: []string{},
	}
	childEventJSON, _ := json.Marshal(childEvent)
	pipe := redisClient.Pipeline()
	pipe.Set(ctx, eventKeyPrefix+childID, childEventJSON, utils.DefaultTTL)
	pipe.ZAdd(ctx, timelineKey, redis.Z{Score: float64(timestamp), Member: childID})
	pipe.ZAdd(ctx, "events:service:"+parent.Service, redis.Z{Score: float64(timestamp), Member: childID})
	_, err := pipe.Exec(ctx)
	return childID, err
}

func createErrorEvent(redisClient *redis.Client, parent *types.Event, handlerName, errorMsg string) ([]string, error) {
	errorEventData := types.HandlerOutputEvent{
		Type: "error_occurred",
		Data: map[string]interface{}{
			"error": errorMsg, "error_type": "HandlerError", "severity": "high",
			"context": map[string]interface{}{"handler": handlerName, "parent_event": parent.ID, "parent_service": parent.Service},
		},
	}
	childID, err := createChildEvent(redisClient, parent, errorEventData)
	if err != nil {
		return nil, err
	}
	parent.ChildIDs = []string{childID}
	_ = updateEvent(redisClient, parent)
	return []string{childID}, nil
}

func updateEvent(redisClient *redis.Client, event *types.Event) error {
	ctx := context.Background()
	eventJSON, _ := json.Marshal(event)
	return redisClient.Set(ctx, eventKeyPrefix+event.ID, eventJSON, utils.DefaultTTL).Err()
}

// SaveInternalEvent saves an event directly to Redis and timelines.
// Used for internal system tracking (e.g., model loads).
func SaveInternalEvent(event types.Event) error {
	if dependencies == nil || dependencies.Redis == nil {
		return fmt.Errorf("executor or redis not initialized")
	}

	ctx := context.Background()
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return err
	}

	// Extract type for indexing
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err != nil {
		return err
	}
	eventType, _ := eventData["type"].(string)

	score := float64(time.Now().UnixMicro()) / 1000000.0
	pipe := dependencies.Redis.Pipeline()

	// Store data
	pipe.Set(ctx, eventKeyPrefix+event.ID, eventJSON, utils.DefaultTTL)

	// Update Timelines
	pipe.ZAdd(ctx, timelineKey, redis.Z{Score: score, Member: event.ID})
	pipe.ZAdd(ctx, "events:service:"+event.Service, redis.Z{Score: score, Member: event.ID})
	if eventType != "" {
		pipe.ZAdd(ctx, "events:type:"+eventType, redis.Z{Score: score, Member: event.ID})
	}

	_, err = pipe.Exec(ctx)
	return err
}
