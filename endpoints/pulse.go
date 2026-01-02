package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// StartCloudPulse starts a background ticker to snapshot system state and push it to Cloud Redis.
func StartCloudPulse(ctx context.Context, cloudClient *redis.Client, interval time.Duration) {
	if cloudClient == nil {
		return
	}

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		log.Printf("Starting Cloud Pulse (Interval: %v)", interval)

		// Run once immediately
		if err := pulse(ctx, cloudClient); err != nil {
			log.Printf("Cloud Pulse initial error: %v", err)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := pulse(ctx, cloudClient); err != nil {
					// Fail silently or log debug
					fmt.Printf("Cloud Pulse error: %v\n", err)
				}
			}
		}
	}()
}

func pulse(ctx context.Context, cloudClient *redis.Client) error {
	pipe := cloudClient.Pipeline()
	ttl := 5 * time.Minute // Short TTL for state to avoid stale data if we go offline

	// 1. System Monitor Snapshot
	// This captures Services (CPU/RAM), Models, and Hardware status
	monitorState := GetSystemMonitorSnapshot()
	monitorJSON, err := json.Marshal(monitorState)
	if err == nil {
		pipe.Set(ctx, "state:system:monitor", monitorJSON, ttl)
	}

	// 2. Processes Snapshot
	// This captures Active Processes, Queue, and History
	procState := GetProcessesSnapshot()
	procJSON, err := json.Marshal(procState)
	if err == nil {
		pipe.Set(ctx, "state:system:processes", procJSON, ttl)
	}

	// Execute Pipeline
	_, err = pipe.Exec(ctx)
	return err
}
