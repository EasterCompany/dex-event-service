package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/discord"
	"github.com/redis/go-redis/v9"
)

// DefaultTTL defines the global expiration for all Redis keys (24 hours).
const DefaultTTL = 24 * time.Hour

// ReportProcess updates a process's state in Redis and synchronizes the Discord status.
func ReportProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string, state string) {
	// --- ABSOLUTE STATE MACHINE LOGIC ---
	// 1. Increment Ref Count
	newCount, _ := redisClient.Incr(ctx, "system:busy_ref_count").Result()

	// 2. If transitioning from Idle (0 -> 1), record Idle time
	if newCount == 1 {
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

	key := fmt.Sprintf("process:info:%s", processID)
	data := map[string]interface{}{
		"channel_id": processID, // Legacy field name for dashboard compatibility
		"state":      state,
		"retries":    0,
		"start_time": time.Now().Unix(),
		"pid":        os.Getpid(),
		"updated_at": time.Now().Unix(),
		"outcome":    "unknown", // Default outcome
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
				// Default to active if success or unknown (we'll improve this)
				redisClient.IncrBy(ctx, "system:metrics:total_active_seconds", duration)
			}

			if histBytes, marshalErr := json.Marshal(p); marshalErr == nil {
				redisClient.LPush(ctx, "process:history", histBytes)
				redisClient.LTrim(ctx, "process:history", 0, 9)
			}
		}
	}

	redisClient.Del(ctx, key)

	// --- ABSOLUTE STATE MACHINE LOGIC ---
	// 1. Decrement Ref Count
	newCount, _ := redisClient.Decr(ctx, "system:busy_ref_count").Result()
	if newCount < 0 {
		redisClient.Set(ctx, "system:busy_ref_count", 0, 0)
		newCount = 0
	}

	// 2. If transitioning to Idle (1 -> 0), mark transition
	if newCount == 0 {
		redisClient.Set(ctx, "system:state", "idle", 0)
		redisClient.Set(ctx, "system:last_transition_ts", time.Now().Unix(), 0)
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
