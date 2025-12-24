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

// ReportProcess updates a process's state in Redis and synchronizes the Discord status.
func ReportProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string, state string) {
	key := fmt.Sprintf("process:info:%s", processID)
	data := map[string]interface{}{
		"channel_id": processID, // Legacy field name for dashboard compatibility
		"state":      state,
		"retries":    0,
		"start_time": time.Now().Unix(),
		"pid":        os.Getpid(),
		"updated_at": time.Now().Unix(),
	}

	jsonBytes, _ := json.Marshal(data)
	// We don't use expiration for standard processes; they must be cleared manually
	redisClient.Set(ctx, key, jsonBytes, 0)

	// Synchronize with Discord
	if discordClient != nil {
		// Activity Type 3 = "Watching"
		discordClient.UpdateBotStatus(state, "online", 3)
	}
}

// ClearProcess removes a process from Redis and attempts to restore an idle Discord status if appropriate.
func ClearProcess(ctx context.Context, redisClient *redis.Client, discordClient *discord.Client, processID string) {
	key := fmt.Sprintf("process:info:%s", processID)
	redisClient.Del(ctx, key)

	// Sync Discord status based on remaining processes
	if discordClient != nil {
		SyncDiscordStatus(ctx, redisClient, discordClient)
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
		// No active processes, revert to standard idle status
		discordClient.UpdateBotStatus("Offline", "online", 3)
	} else if latestProcessState != "" {
		// Update to the most recently updated process state
		discordClient.UpdateBotStatus(latestProcessState, "online", 3)
	}
}
