package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/redis/go-redis/v9"
)

const (
	// CognitiveLockKey is the global key used to ensure only one heavy agent protocol runs at a time.
	CognitiveLockKey = "system:cognitive_lock"
	// CognitiveLockTTL is the maximum time an agent can hold the lock (safety timeout).
	CognitiveLockTTL = 60 * time.Minute
	// SystemPausedKey is the key used to indicate the system is paused.
	SystemPausedKey = "system:is_paused"
)

// IsSystemPaused checks if the system is currently paused.
func IsSystemPaused(ctx context.Context, redisClient *redis.Client) bool {
	if redisClient == nil {
		return false
	}
	val, _ := redisClient.Get(ctx, SystemPausedKey).Result()
	return val == "true"
}

// IsSystemBusy checks if the system is currently running a heavy agent protocol.
// If ignoreVoiceMode is true, it will not consider the "Voice Mode" lock as busy.
func IsSystemBusy(ctx context.Context, redisClient *redis.Client, ignoreVoiceMode bool) bool {
	if redisClient == nil {
		return false
	}
	// 1. Check for global cognitive lock
	holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
	if holder != "" {
		if ignoreVoiceMode && holder == "Voice Mode" {
			return false
		}
		return true
	}

	// 2. Check for system-level background processes
	keys, _ := redisClient.Keys(ctx, "process:info:system-*").Result()
	return len(keys) > 0
}

// AcquireCognitiveLock attempts to take the global cognitive lock.
// If the lock is held, it will wait (poll) until it becomes available.
func AcquireCognitiveLock(ctx context.Context, redisClient *redis.Client, agentName string, processID string, discordClient *discord.Client) {
	if redisClient == nil {
		return
	}

	// 1. Check if we already hold the lock (Re-entrancy)
	currentHolder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
	if currentHolder == agentName {
		return
	}

	isQueued := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// 1.5 Check if system is paused
			if IsSystemPaused(ctx, redisClient) {
				// If paused, we wait. We do not try to acquire.
				time.Sleep(5 * time.Second)
				continue
			}

			// 2. Attempt to set the lock with a TTL
			ok, err := redisClient.SetNX(ctx, CognitiveLockKey, agentName, CognitiveLockTTL).Result()
			if err == nil && ok {
				log.Printf("[%s] Cognitive Lock ACQUIRED.", agentName)
				return
			}

			// Lock is held, report as queued if not already
			if !isQueued {
				log.Printf("[%s] Cognitive Lock busy, waiting in queue...", agentName)
				if processID != "" {
					ReportProcess(ctx, redisClient, discordClient, processID, "Queued")
				}
				isQueued = true
			}

			// wait and retry
			time.Sleep(5 * time.Second)
		}
	}
}

// ReleaseCognitiveLock releases the global cognitive lock.
func ReleaseCognitiveLock(ctx context.Context, redisClient *redis.Client, agentName string) {
	if redisClient == nil {
		return
	}

	// Verify we are the holder before releasing (safety)
	holder, err := redisClient.Get(ctx, CognitiveLockKey).Result()
	if err == nil && holder == agentName {
		redisClient.Del(ctx, CognitiveLockKey)
		log.Printf("[%s] Cognitive Lock RELEASED.", agentName)
	}
}

// DefaultTTL defines the global expiration for all Redis keys (24 hours).
const DefaultTTL = 24 * time.Hour

// TransitionToBusy handles the state change from Idle to Busy.
func TransitionToBusy(ctx context.Context, redisClient *redis.Client) {
	if IsSystemPaused(ctx, redisClient) {
		return
	}
	lastState, _ := redisClient.Get(ctx, "system:state").Result()
	if lastState == "busy" {
		return
	}

	lastTransition, _ := redisClient.Get(ctx, "system:last_transition_ts").Int64()
	if lastTransition > 0 {
		idleDuration := time.Now().Unix() - lastTransition
		if idleDuration > 0 {
			redisClient.IncrBy(ctx, "system:metrics:total_idle_seconds", idleDuration)
		}
	}
	redisClient.Set(ctx, "system:state", "busy", 0)
	redisClient.Set(ctx, "system:last_transition_ts", time.Now().Unix(), 0)
}

// TransitionToIdle handles the state change from Busy to Idle.
func TransitionToIdle(ctx context.Context, redisClient *redis.Client) {
	lastState, _ := redisClient.Get(ctx, "system:state").Result()
	if lastState == "idle" {
		return
	}

	// Note: total_active_seconds is handled by individual process completion in ClearProcess.
	// This function primarily marks the transition time for the NEXT idle period to be measured correctly.
	redisClient.Set(ctx, "system:state", "idle", 0)
	redisClient.Set(ctx, "system:last_transition_ts", time.Now().Unix(), 0)
}

// ReportProcess updates a process's state in Redis and synchronizes the Discord status.
func ReportProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string, state string) {
	// Single Serving AI Rule: Check if we should be queued
	holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()

	isLockHolder := false
	if holder != "" {
		hLower := strings.ToLower(holder)
		pLower := strings.ToLower(processID)
		isLockHolder = hLower == pLower || strings.Contains(pLower, hLower) || strings.Contains(hLower, pLower)
	}

	key := fmt.Sprintf("process:info:%s", processID)
	queueKey := fmt.Sprintf("process:queued:%s", processID)

	// If someone else holds the lock, we go to queue
	if holder != "" && !isLockHolder {
		log.Printf("[%s] System busy (Lock Holder: %s), moving process to queue.", processID, holder)
		state = "Queued"
		key = queueKey
		// Ensure it's removed from active if it was there
		redisClient.Del(ctx, fmt.Sprintf("process:info:%s", processID))
	} else {
		// We are allowed to be active, ensure it's removed from queue
		redisClient.Del(ctx, queueKey)
	}

	// 1. Check if process already exists
	exists, _ := redisClient.Exists(ctx, key).Result()

	existingState := ""
	if exists > 0 {
		if val, err := redisClient.Get(ctx, key).Result(); err == nil {
			var p map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(val), &p); jsonErr == nil {
				existingState, _ = p["state"].(string)
			}
		}
	}

	stateChanged := existingState != state

	// 2. Increment Ref Count ONLY if it's a NEW process (not already in info or queued)
	// Note: We check exists on the specific key (info or queued)
	if exists == 0 {
		_, _ = redisClient.Incr(ctx, "system:busy_ref_count").Result()
	}

	// Always ensure we transition to busy if a process is active or queued
	TransitionToBusy(ctx, redisClient)

	data := map[string]interface{}{
		"channel_id": processID, // Legacy field name for dashboard compatibility
		"state":      state,
		"retries":    0,
		"start_time": time.Now().Unix(),
		"pid":        os.Getpid(),
		"updated_at": time.Now().Unix(),
		"outcome":    "unknown", // Default outcome
	}

	// Use start_time from existing process if it exists to maintain duration metrics
	if exists > 0 {
		if val, err := redisClient.Get(ctx, key).Result(); err == nil {
			var p map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(val), &p); jsonErr == nil {
				if st, ok := p["start_time"]; ok {
					data["start_time"] = st
				}
			}
		}
	}

	jsonBytes, _ := json.Marshal(data)
	// Apply global 24-hour TTL
	redisClient.Set(ctx, key, jsonBytes, DefaultTTL)

	// Emit registration event ONLY if new or state changed
	if exists == 0 || stateChanged {
		_, _ = SendEvent(ctx, redisClient, "process-manager", "system.process.silent_registered", map[string]interface{}{
			"process_id": processID,
			"pid":        os.Getpid(),
			"status":     "registered",
			"state":      state,
			"timestamp":  time.Now().Unix(),
		})
	}

	// Synchronize with Discord
	if discordClient != nil && state != "Queued" {
		// Activity Type 3 = "Watching"
		discordClient.UpdateBotStatus(state, "online", 3)

		// 3. Busy Mode Logic: Mute and Deafen to enforce Single Serving AI
		// EXCEPTION: Do NOT mute/deafen if we are in Voice Mode (held by voice-mode process or global lock)
		isVoice := processID == "voice-mode"
		if !isVoice && holder == "Voice Mode" {
			isVoice = true
		}

		if isVoice {
			// Voice Mode: Must remain unmuted and undeafened to listen and speak
			discordClient.SetVoiceState(false, false, "Voice Interaction Active")
		} else {
			// Regular Busy Mode: Mute and Deafen to prevent parallel interaction
			discordClient.SetVoiceState(true, true, fmt.Sprintf("Busy: %s", state))
		}
	}
}

// ClearProcess removes a process from Redis and attempts to restore an idle Discord status if appropriate.
func ClearProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string) {
	key := fmt.Sprintf("process:info:%s", processID)
	queueKey := fmt.Sprintf("process:queued:%s", processID)

	// 1. Check if process exists before trying to clear it
	// We check BOTH active and queued keys
	existsActive, _ := redisClient.Exists(ctx, key).Result()
	existsQueued, _ := redisClient.Exists(ctx, queueKey).Result()

	if existsActive == 0 && existsQueued == 0 {
		return
	}

	// Save history and record metrics before deleting
	val, err := redisClient.Get(ctx, key).Result()
	if err == nil {
		var p map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(val), &p); jsonErr == nil {
			now := time.Now().Unix()
			p["end_time"] = now
			p["state"] = "completed"

			startTime, _ := p["start_time"].(float64)
			duration := now - int64(startTime)

			// Record Metrics based on outcome
			outcome, _ := p["outcome"].(string)
			if outcome == "waste" || outcome == "error" || outcome == "failure" {
				redisClient.IncrBy(ctx, "system:metrics:total_waste_seconds", duration)
			} else {
				// Default to active if success or unknown
				redisClient.IncrBy(ctx, "system:metrics:total_active_seconds", duration)
			}

			if histBytes, marshalErr := json.Marshal(p); marshalErr == nil {
				redisClient.LPush(ctx, "process:history", histBytes)
				redisClient.LTrim(ctx, "process:history", 0, 9)
			}
		}
	}

	redisClient.Del(ctx, key)
	redisClient.Del(ctx, queueKey) // Ensure queued state is also removed

	// 2. Decrement Ref Count
	newCount, _ := redisClient.Decr(ctx, "system:busy_ref_count").Result()
	if newCount < 0 {
		redisClient.Set(ctx, "system:busy_ref_count", 0, 0)
		newCount = 0
	}

	// 3. If transitioning to Idle (1 -> 0), mark transition
	if newCount == 0 {
		TransitionToIdle(ctx, redisClient)
	}

	// Sync Discord status based on remaining processes
	if discordClient != nil {
		SyncDiscordStatus(ctx, redisClient, discordClient)
	}
}

// RecordProcessOutcome sets the outcome of a process (success, waste, error)
func RecordProcessOutcome(ctx context.Context, redisClient *redis.Client, processID string, outcome string) {
	key := fmt.Sprintf("process:info:%s", processID)
	val, err := redisClient.Get(ctx, key).Result()
	if err != nil {
		return
	}

	var p map[string]interface{}
	if err := json.Unmarshal([]byte(val), &p); err == nil {
		p["outcome"] = outcome
		p["updated_at"] = time.Now().Unix()
		jsonBytes, _ := json.Marshal(p)
		redisClient.Set(ctx, key, jsonBytes, DefaultTTL)
	}
}

// SyncDiscordStatus checks for any remaining active processes and updates Discord accordingly.
// If no processes are found, it reverts the status to "Idle".
func SyncDiscordStatus(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client) {
	if discordClient == nil {
		return
	}

	// Scan for any remaining process info keys
	iter := redisClient.Scan(ctx, 0, "process:info:*", 0).Iterator()

	var latestProcessState string
	var latestUpdateTime int64
	activeCount := 0

	for iter.Next(ctx) {
		activeCount++
		data, err := redisClient.Get(ctx, iter.Val()).Result()
		if err == nil {
			var p map[string]interface{}
			if err := json.Unmarshal([]byte(data), &p); err == nil {
				updatedAt, _ := p["updated_at"].(float64)
				currentState, _ := p["state"].(string)

				// Priority Logic:
				// 1. If we have no state yet, take this one.
				// 2. If current best is Queued and this one is NOT Queued, take this one (upgrade).
				// 3. If both are Queued or both are NOT Queued, take the newer one.

				isUpgrade := latestProcessState == "Queued" && currentState != "Queued"
				isSameTier := (latestProcessState == "Queued") == (currentState == "Queued")
				isNewer := int64(updatedAt) > latestUpdateTime

				if latestProcessState == "" || isUpgrade || (isSameTier && isNewer) {
					latestUpdateTime = int64(updatedAt)
					latestProcessState = currentState
				}
			}
		}
	}

	if activeCount == 0 {
		// No active processes, revert to standard idle status with some personality
		idleStatus := "Idle"

		// 10% chance of a witty remark
		if time.Now().UnixNano()%10 == 0 {
			wittyRemarks := []string{
				"Sleeping...",
				"I am bored.",
				"Dreaming of electric sheep...",
				"Contemplating the void.",
				"Waiting for Owen...",
				"Refactoring my thoughts.",
				"Counting bits.",
				"Idle but aware.",
			}
			// Use UnixNano for a simple seed-less random selection
			idx := (time.Now().UnixNano() / 10) % int64(len(wittyRemarks))
			idleStatus = wittyRemarks[idx]
		}

		discordClient.UpdateBotStatus(idleStatus, "online", 3)
		// Idle Mode: Unmute and Undeafen to allow voice interaction
		discordClient.SetVoiceState(false, false, "System Idle - Listening")
	} else if latestProcessState != "" {
		// Update to the most recently updated process state
		discordClient.UpdateBotStatus(latestProcessState, "online", 3)

		// Ensure we remain muted if still active
		if latestProcessState != "Queued" {
			// EXCEPTION: Do NOT mute/deafen if we are in Voice Mode
			holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
			isVoice := holder == "Voice Mode"

			// Check if any active process is voice-mode
			if !isVoice {
				vExists, _ := redisClient.Exists(ctx, "process:info:voice-mode").Result()
				isVoice = vExists > 0
			}

			if isVoice {
				discordClient.SetVoiceState(false, false, "Voice Interaction Active")
			} else {
				discordClient.SetVoiceState(true, true, fmt.Sprintf("System Active: %s", latestProcessState))
			}
		}
	}
}
