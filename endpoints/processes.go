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
	if RDB == nil {
		http.Error(w, "Redis client not initialized", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID    string `json:"id"`
		State string `json:"state"`
		PID   int    `json:"pid"`
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
	exists := false
	val, err := RDB.Get(ctx, key).Result()
	if err == nil {
		if jsonErr := json.Unmarshal([]byte(val), &pi); jsonErr == nil {
			exists = true
		}
	}

	stateChanged := pi.State != req.State

	if pi.StartTime == 0 {
		pi.StartTime = time.Now().Unix()
	}

	pi.ChannelID = req.ID
	pi.State = req.State
	pi.PID = req.PID
	pi.UpdatedAt = time.Now().Unix()

	jsonBytes, _ := json.Marshal(pi)
	if err := RDB.Set(ctx, key, jsonBytes, utils.DefaultTTL).Err(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save process info: %v", err), http.StatusInternalServerError)
		return
	}

	// Update system state
	utils.TransitionToBusy(ctx, RDB)

	// ONLY increment ref count if it's a NEW process
	if !exists {
		RDB.Incr(ctx, "system:busy_ref_count")
	}

	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("Process registered successfully"))

	// Emit registration event ONLY if new or state changed
	if !exists || stateChanged {
		_, _ = utils.SendEvent(ctx, RDB, "process-manager", "system.process.registered", map[string]interface{}{
			"process_id": req.ID,
			"pid":        req.PID,
			"status":     "registered",
			"state":      req.State,
			"timestamp":  time.Now().Unix(),
		})
	}
}

// HandleProcessUnregistration removes a process from Redis.
func HandleProcessUnregistration(w http.ResponseWriter, r *http.Request) {
	if RDB == nil {
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

	// Save to history before deleting
	val, err := RDB.Get(ctx, key).Result()
	if err == nil {
		var pi ProcessInfo
		if jsonErr := json.Unmarshal([]byte(val), &pi); jsonErr == nil {
			pi.EndTime = time.Now().Unix()
			pi.State = "completed"

			if histBytes, marshalErr := json.Marshal(pi); marshalErr == nil {
				RDB.LPush(ctx, "process:history", histBytes)
				RDB.LTrim(ctx, "process:history", 0, 19)
			}
		}
	}

	if err := RDB.Del(ctx, key).Err(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete process info: %v", err), http.StatusInternalServerError)
		return
	}

	// Update system state
	newCount, _ := RDB.Decr(ctx, "system:busy_ref_count").Result()
	if newCount <= 0 {
		RDB.Set(ctx, "system:busy_ref_count", 0, 0)
		utils.TransitionToIdle(ctx, RDB)
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Process unregistered successfully"))

	// Emit unregistration event
	_, _ = utils.SendEvent(ctx, RDB, "process-manager", "system.process.unregistered", map[string]interface{}{
		"process_id": id,
		"status":     "unregistered",
		"timestamp":  time.Now().Unix(),
	})
}
