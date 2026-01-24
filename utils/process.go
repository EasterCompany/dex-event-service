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
// If ignoreVoiceMode is true, it will not consider the "Voice Mode" or "voice-mode" lock as busy.
// If testID is non-empty, it bypasses the global cognitive lock (used for simulations).
func IsSystemBusy(ctx context.Context, redisClient *redis.Client, ignoreVoiceMode bool, processID string) bool {
	if redisClient == nil {
		return false
	}

	// 1. Simulations (events with test_id) and CLI are allowed to bypass the cognitive lock
	// because they are often running WHILE the system is locked for validation.
	isTestProcess := strings.HasPrefix(processID, "test-") || processID == "dex-test-service" || processID == "dex-cli"

	// 2. Check for global cognitive lock
	holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
	if holder != "" {
		if holder == "SYSTEM_VALIDATION_ACTIVE" {
			if isTestProcess {
				return false // Only test processes can bypass the validation lock
			}
			return true // Background tasks (Compressor, etc.) must wait
		}
		if ignoreVoiceMode && (holder == "Voice Mode" || holder == "voice-mode") {
			// Voice is allowed, continue check for other processes
		} else {
			return true
		}
	}

	// 2. Check for system-level background processes
	keys, _ := redisClient.Keys(ctx, "process:info:system-*").Result()

	// If ignoreVoiceMode is true and voice-mode is the only other process, it's not busy.
	// We count non-voice system processes.
	realBusyCount := 0
	for _, k := range keys {
		if ignoreVoiceMode && strings.HasSuffix(k, ":voice-mode") {
			continue
		}
		realBusyCount++
	}

	return realBusyCount > 0
}

// AcquireCognitiveLock attempts to take the global cognitive lock.
// If the lock is held by another agent, it will wait (poll) until it becomes available.
// If the lock is held by "PAUSED", it will override it to allow human interaction.
func AcquireCognitiveLock(ctx context.Context, redisClient *redis.Client, agentName string, processID string, discordClient *discord.Client) {
	if redisClient == nil {
		return
	}

	// Simulations and CLI are allowed to bypass the lock
	isTestProcess := strings.HasPrefix(processID, "test-") || processID == "dex-test-service" || processID == "dex-cli"
	if isTestProcess {
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
			// 2. Check if lock is "PAUSED" - if so, we can override it
			val, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
			if val == "PAUSED" {
				// We take the lock even if paused because this is a reactive interaction
				redisClient.Set(ctx, CognitiveLockKey, agentName, CognitiveLockTTL)
				log.Printf("[%s] Cognitive Lock acquired by overriding PAUSE.", agentName)
				return
			}

			// 3. Attempt to set the lock with a TTL
			ok, err := redisClient.SetNX(ctx, CognitiveLockKey, agentName, CognitiveLockTTL).Result()
			if err == nil && ok {
				log.Printf("[%s] Cognitive Lock ACQUIRED.", agentName)
				return
			}

			// Lock is held by another ACTIVE agent, report as queued if not already
			if !isQueued {
				log.Printf("[%s] System busy (Locked by %s), waiting in queue...", agentName, val)
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
// If the system is still paused, it restores the "PAUSED" lock state.
func ReleaseCognitiveLock(ctx context.Context, redisClient *redis.Client, agentName string) {
	if redisClient == nil {
		return
	}

	// Verify we are the holder before releasing (safety)
	holder, err := redisClient.Get(ctx, CognitiveLockKey).Result()
	if err == nil && holder == agentName {
		if IsSystemPaused(ctx, redisClient) {
			// Restore the PAUSED indicator for the dashboard
			redisClient.Set(ctx, CognitiveLockKey, "PAUSED", 0)
			log.Printf("[%s] Cognitive Lock RELEASED (Restored PAUSE).", agentName)
		} else {
			redisClient.Del(ctx, CognitiveLockKey)
			log.Printf("[%s] Cognitive Lock RELEASED.", agentName)
		}
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
	now := time.Now().Unix()

	if lastTransition > 0 {
		idleDuration := now - lastTransition
		if idleDuration > 0 {
			redisClient.IncrBy(ctx, "system:metrics:total_idle_seconds", idleDuration)
		}
	} else {
		// If it was never set, set it now so the NEXT transition is accurate
		redisClient.Set(ctx, "system:last_transition_ts", now, 0)
	}

	redisClient.Set(ctx, "system:state", "busy", 0)
	redisClient.Set(ctx, "system:last_transition_ts", now, 0)
}

// TransitionToIdle handles the state change from Busy to Idle.
func TransitionToIdle(ctx context.Context, redisClient *redis.Client) {
	lastState, _ := redisClient.Get(ctx, "system:state").Result()
	now := time.Now().Unix()

	if lastState == "idle" || lastState == "paused" {
		// Even if already idle, ensure the transition TS is set if it was missing
		ts, _ := redisClient.Exists(ctx, "system:last_transition_ts").Result()
		if ts == 0 {
			redisClient.Set(ctx, "system:last_transition_ts", now, 0)
		}
		return
	}

	targetState := "idle"
	if IsSystemPaused(ctx, redisClient) {
		targetState = "paused"
	}

	// Note: total_active_seconds is handled by individual process completion in ClearProcess.
	// This function primarily marks the transition time for the NEXT idle period to be measured correctly.
	redisClient.Set(ctx, "system:state", targetState, 0)
	redisClient.Set(ctx, "system:last_transition_ts", now, 0)
}

// ReportProcess updates a process's state in Redis and synchronizes the Discord status.
func ReportProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string, state string) {
	// Single Serving AI Rule: Check if we should be queued
	holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()

	isTestProcess := strings.HasPrefix(processID, "test-") || processID == "dex-test-service" || processID == "dex-cli"

	isLockHolder := false
	if holder != "" {
		if holder == "SYSTEM_VALIDATION_ACTIVE" && isTestProcess {
			isLockHolder = true // Only test processes bypass the validation lock
		} else {
			hLower := strings.ToLower(holder)
			pLower := strings.ToLower(processID)
			isLockHolder = hLower == pLower || strings.Contains(pLower, hLower) || strings.Contains(hLower, pLower)
		}
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

	// 1. Check if process already exists in ANY state (Active or Queued)
	// This prevents double-incrementing system:busy_ref_count when transitioning from queue to info
	existsActive, _ := redisClient.Exists(ctx, fmt.Sprintf("process:info:%s", processID)).Result()
	existsQueued, _ := redisClient.Exists(ctx, fmt.Sprintf("process:queued:%s", processID)).Result()
	alreadyKnown := existsActive > 0 || existsQueued > 0

	existingState := ""
	if existsActive > 0 {
		if val, err := redisClient.Get(ctx, fmt.Sprintf("process:info:%s", processID)).Result(); err == nil {
			var p map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(val), &p); jsonErr == nil {
				existingState, _ = p["state"].(string)
			}
		}
	} else if existsQueued > 0 {
		if val, err := redisClient.Get(ctx, fmt.Sprintf("process:queued:%s", processID)).Result(); err == nil {
			var p map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(val), &p); jsonErr == nil {
				existingState, _ = p["state"].(string)
			}
		}
	}

	stateChanged := existingState != state

	// 2. Increment Ref Count ONLY if it's a completely NEW process ID
	if !alreadyKnown {
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
	if alreadyKnown {
		sourceKey := fmt.Sprintf("process:info:%s", processID)
		if existsQueued > 0 {
			sourceKey = fmt.Sprintf("process:queued:%s", processID)
		}
		if val, err := redisClient.Get(ctx, sourceKey).Result(); err == nil {
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
	if !alreadyKnown || stateChanged {
		_, _ = SendEvent(ctx, redisClient, "process-manager", "system.process.registered", map[string]interface{}{
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

// SyncSystemState enforces the correct busy/idle state based on active processes and cognitive lock.
// This is used as a self-healing mechanism in the status building loop.
func SyncSystemState(ctx context.Context, redisClient *redis.Client) string {
	if redisClient == nil {
		return "idle"
	}

	// 1. Check if paused (highest priority override)
	isPaused, _ := redisClient.Get(ctx, "system:is_paused").Result()
	if isPaused == "true" {
		currentState, _ := redisClient.Get(ctx, "system:state").Result()
		if currentState != "paused" {
			redisClient.Set(ctx, "system:state", "paused", 0)
		}
		return "paused"
	}

	// 2. Check if we should be busy
	busy := false

	// A. Global Cognitive Lock
	holder, _ := redisClient.Get(ctx, CognitiveLockKey).Result()
	if holder != "" && holder != "PAUSED" {
		busy = true
	}

	// B. Active Processes
	if !busy {
		keys, _ := redisClient.Keys(ctx, "process:info:*").Result()
		if len(keys) > 0 {
			busy = true
		}
	}

	// C. Queued Processes
	if !busy {
		keys, _ := redisClient.Keys(ctx, "process:queued:*").Result()
		if len(keys) > 0 {
			busy = true
		}
	}

	// 3. Apply state transition if needed
	lastState, _ := redisClient.Get(ctx, "system:state").Result()

	if busy && lastState != "busy" {
		TransitionToBusy(ctx, redisClient)
		return "busy"
	} else if !busy && lastState != "idle" {
		TransitionToIdle(ctx, redisClient)
		return "idle"
	}

	if lastState == "" {
		return "idle"
	}
	return lastState
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
