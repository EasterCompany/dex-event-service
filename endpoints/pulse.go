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

	go func() {
		// Run initial pulse immediately on boot
		if err := pulse(ctx, cloudClient); err != nil {
			log.Printf("Cloud Pulse initial boot error: %v", err)
		}

		log.Printf("Aligning Cloud Pulse to :00 second mark (Interval: %v)", interval)

		// 1. Alignment Logic: Wait until the start of the next minute
		now := time.Now()
		nextMinute := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextMinute)):
			// Aligned
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run first aligned pulse
		if err := pulse(ctx, cloudClient); err != nil {
			log.Printf("Cloud Pulse initial aligned error: %v", err)
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
	ttl := 5 * time.Minute // Short TTL for state to avoid stale data if we go offline

	// Generate Monolithic Snapshot
	snapshot := GetDashboardSnapshot()
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("failed to marshal dashboard snapshot: %w", err)
	}

	// Single atomic write to Upstash
	err = cloudClient.Set(ctx, "state:dashboard:full", snapshotJSON, ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to write dashboard snapshot to cloud: %w", err)
	}

	return nil
}
