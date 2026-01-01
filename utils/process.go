package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/redis/go-redis/v9"
)

const (
	// CognitiveLockKey is the global key used to ensure only one heavy agent protocol runs at a time.
	CognitiveLockKey = "system:cognitive_lock"
	// CognitiveLockTTL is the maximum time an agent can hold the lock (safety timeout).
	CognitiveLockTTL = 10 * time.Minute
)

// AcquireCognitiveLock attempts to take the global cognitive lock.
// If the lock is held, it will wait (poll) until it becomes available.
func AcquireCognitiveLock(ctx context.Context, redisClient *redis.Client, agentName string) {
	if redisClient == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Attempt to set the lock with a TTL
			ok, err := redisClient.SetNX(ctx, CognitiveLockKey, agentName, CognitiveLockTTL).Result()
			if err == nil && ok {
				log.Printf("[%s] Cognitive Lock ACQUIRED.", agentName)
				return
			}

			// Lock is held, wait and retry
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
	key := fmt.Sprintf("process:info:%s", processID)

	// 1. Check if process already exists
	exists, _ := redisClient.Exists(ctx, key).Result()

	// 2. Increment Ref Count ONLY if it's a new process
	if exists == 0 {
		newCount, _ := redisClient.Incr(ctx, "system:busy_ref_count").Result()
		// If transitioning from Idle (0 -> 1), handle transition
		if newCount == 1 {
			TransitionToBusy(ctx, redisClient)
		}
	}

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

	// Synchronize with Discord
	if discordClient != nil {
		// Activity Type 3 = "Watching"
		discordClient.UpdateBotStatus(state, "online", 3)
	}
}

// ClearProcess removes a process from Redis and attempts to restore an idle Discord status if appropriate.
func ClearProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string) {
	key := fmt.Sprintf("process:info:%s", processID)

	// 1. Check if process exists before trying to clear it
	exists, _ := redisClient.Exists(ctx, key).Result()
	if exists == 0 {
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
				if int64(updatedAt) > latestUpdateTime {
					latestUpdateTime = int64(updatedAt)
					latestProcessState, _ = p["state"].(string)
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
	} else if latestProcessState != "" {
		// Update to the most recently updated process state
		discordClient.UpdateBotStatus(latestProcessState, "online", 3)
	}
}
