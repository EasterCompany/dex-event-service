package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/gorilla/mux"
)

// HandleProcessRegistration registers or updates a process in Redis.
func HandleProcessRegistration(w http.ResponseWriter, r *http.Request) {
	if redisClient == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ID == "" {
		http.Error(w, "Process ID is required", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("process:info:%s", req.ID)

	// Try to get existing process to preserve start time if updating
	var pi ProcessInfo
	val, err := redisClient.Get(ctx, key).Result()
	if err == nil {
		_ = json.Unmarshal([]byte(val), &pi)
	}

	if pi.StartTime == 0 {
		pi.StartTime = time.Now().Unix()
	}

	pi.ChannelID = req.ID
	pi.State = req.State
	pi.UpdatedAt = time.Now().Unix()

	jsonBytes, _ := json.Marshal(pi)
	if err := redisClient.Set(ctx, key, jsonBytes, utils.DefaultTTL).Err(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save process info: %v", err), http.StatusInternalServerError)
		return
	}

	// Update Discord status if possible (similar to ReportProcess)
	// We need access to the Discord client here.
	// Since endpoints is a package, we might need a way to pass dependencies.
	// But dex-event-service's endpoints package doesn't seem to have a global discord client.
	// Wait, internal/handlers has it.

	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("Process registered successfully"))

	// Emit Event
	utils.SendEvent(ctx, redisClient, "process-manager", "system.process.registered", map[string]interface{}{
		"id":    req.ID,
		"state": req.State,
	})
}

// HandleProcessUnregistration removes a process from Redis.
func HandleProcessUnregistration(w http.ResponseWriter, r *http.Request) {
	if redisClient == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	if id == "" {
		http.Error(w, "Process ID is required", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("process:info:%s", id)

	if err := redisClient.Del(ctx, key).Err(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete process info: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Process unregistered successfully"))

	// Emit Event
	utils.SendEvent(ctx, redisClient, "process-manager", "system.process.unregistered", map[string]interface{}{
		"id": id,
	})
}
