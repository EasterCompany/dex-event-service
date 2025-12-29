package utils

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// SendEvent is an internal helper to store an event in Redis and update all relevant timelines.
func SendEvent(ctx context.Context, redisClient *redis.Client, service string, eventType string, eventData map[string]interface{}) {
	eventID := uuid.New().String()
	timestamp := time.Now().Unix()

	eventData["type"] = eventType
	eventJSON, _ := json.Marshal(eventData)

	// Use an anonymous struct to avoid importing the types package (prevents import cycle)
	event := struct {
		ID        string          `json:"id"`
		Service   string          `json:"service"`
		Event     json.RawMessage `json:"event"`
		Timestamp int64           `json:"timestamp"`
	}{
		ID:        eventID,
		Service:   service,
		Event:     eventJSON,
		Timestamp: timestamp,
	}

	fullEventJSON, _ := json.Marshal(event)
	pipe := redisClient.Pipeline()

	// 1. Store the event data
	pipe.Set(ctx, "event:"+eventID, fullEventJSON, DefaultTTL)

	// 2. Add to global timeline
	pipe.ZAdd(ctx, "events:timeline", redis.Z{Score: float64(timestamp), Member: eventID})

	// 3. Add to service timeline
	pipe.ZAdd(ctx, "events:service:"+service, redis.Z{Score: float64(timestamp), Member: eventID})

	// 4. Add to type timeline
	pipe.ZAdd(ctx, "events:type:"+eventType, redis.Z{Score: float64(timestamp), Member: eventID})

	// 5. Add to channel timeline if applicable
	var channelID string
	if cid, ok := eventData["channel_id"].(string); ok {
		channelID = cid
	} else if tid, ok := eventData["target_channel"].(string); ok {
		channelID = tid
	}

	if channelID != "" {
		pipe.ZAdd(ctx, "events:channel:"+channelID, redis.Z{Score: float64(timestamp), Member: eventID})
	}

	_, _ = pipe.Exec(ctx)
}
