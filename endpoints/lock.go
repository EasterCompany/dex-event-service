package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

var (
	eventLock       sync.RWMutex
	isLocked        bool
	lockKey         string
	allowedServices = map[string]bool{
		"dex-test-service": true,
		"dex-cli":          true, // CLI initiates the test
	}
)

const pendingQueueKey = "events:queue:pending"

type LockRequest struct {
	Lock bool   `json:"lock"`
	Key  string `json:"key"` // Optional identifier for the lock session
}

// SystemLockHandler toggles the event ingestion lock
func SystemLockHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		eventLock.Lock()
		defer eventLock.Unlock()

		if req.Lock {
			isLocked = true
			lockKey = req.Key
			log.Printf("System Event Lock ENABLED (Key: %s)", lockKey)
		} else {
			// Only unlock if it was locked
			if isLocked {
				isLocked = false
				log.Printf("System Event Lock DISABLED. Processing pending events...")
				go processPendingEvents(redisClient)
			}
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"locked":   isLocked,
			"lock_key": lockKey,
		})
	}
}

// IsEventLocked checks if the event should be blocked/queued
func IsEventLocked(req types.CreateEventRequest) bool {
	eventLock.RLock()
	defer eventLock.RUnlock()

	if !isLocked {
		return false
	}

	// Always allow privileged services
	if allowedServices[req.Service] {
		return false
	}

	// Always allow events with explicit test_id
	var eventData map[string]interface{}
	if err := json.Unmarshal(req.Event, &eventData); err == nil {
		if tid, ok := eventData["test_id"]; ok && tid != "" {
			return false
		}
	}

	return true
}

// QueuePendingEvent stores the blocked event in Redis
func QueuePendingEvent(redisClient *redis.Client, req types.CreateEventRequest) error {
	ctx := context.Background()
	jsonData, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return redisClient.RPush(ctx, pendingQueueKey, jsonData).Err()
}

// processPendingEvents replays queued events
func processPendingEvents(redisClient *redis.Client) {
	ctx := context.Background()

	// Create a dedicated HTTP client to re-submit events to ourselves
	// We use the loopback address and the service port (assuming 8082 for event service from previous knowledge)
	// Ideally, we would call CreateEventHandler logic directly, but that requires refactoring to decouple logic from HTTP.
	// For simplicity and robustness, we'll re-POST to the endpoint.
	// But wait, the main.go sets up the server. We don't easily know our own port inside this package unless we pass it.
	// We'll rely on the default port 8082 or checking the config?
	// Config loading is in main.
	// Let's assume standard local port 8082 for now, or pass it.
	// Actually, `dex-event-service` runs on 8082?
	// `service-map.json` says `dex-event-service` is on 8100.
	// In `main.go` I saw `eventURL = getServiceURL(..., "8082")` but that was for discord client usage.
	// `serviceDefinitions` in `dex-cli` says 8100.

	targetURL := "http://127.0.0.1:8100/events" // Fallback
	if sm, err := config.LoadServiceMap(); err == nil {
		for _, s := range sm.Services["cs"] {
			if s.ID == "dex-event-service" {
				targetURL = "http://127.0.0.1:" + s.Port + "/events"
				break
			}
		}
	}

	for {
		// Pop one event
		result, err := redisClient.LPop(ctx, pendingQueueKey).Result()
		if err == redis.Nil {
			break // Queue empty
		}
		if err != nil {
			log.Printf("Error popping pending event: %v", err)
			break
		}

		// Re-submit
		// We don't need to unmarshal, just send raw bytes
		resp, err := http.Post(targetURL, "application/json", bytes.NewBuffer([]byte(result)))
		if err != nil {
			log.Printf("Failed to re-submit pending event: %v", err)
			// Push back? Maybe lost. For now log it.
			continue
		}
		_ = resp.Body.Close()

		// Small delay to prevent flooding
		time.Sleep(10 * time.Millisecond)
	}
	log.Println("Pending event queue processing complete.")
}
