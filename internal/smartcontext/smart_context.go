package smartcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

// Get fetches the last 25 events, summarizes the older 20, and keeps the recent 5 raw.
// It returns a formatted string ready for injection into the prompt.
func Get(ctx context.Context, redisClient *redis.Client, ollamaClient *ollama.Client, channelID string, summaryModel string) (string, error) {
	// 1. Fetch last 25 events (reverse range because ZAdd uses timestamp score)
	// ZRevRange gets highest scores (newest) first.
	eventIDs, err := redisClient.ZRevRange(ctx, "events:channel:"+channelID, 0, 24).Result()
	if err != nil {
		return "", err
	}

	if len(eventIDs) == 0 {
		return "", nil
	}

	// 2. Fetch event data
	var events []types.Event
	for _, id := range eventIDs {
		data, err := redisClient.Get(ctx, "event:"+id).Result()
		if err == nil {
			var evt types.Event
			if err := json.Unmarshal([]byte(data), &evt); err == nil {
				events = append(events, evt)
			}
		}
	}

	// 3. Reorder to chronological (Oldest -> Newest)
	// Because we fetched ZRevRange (Newest -> Oldest), we need to reverse.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	// 4. Split logic
	total := len(events)
	if total <= 5 {
		// Not enough history to summarize, just return raw text
		return formatEventsBlock(events), nil
	}

	// Split: older (0 to total-5), recent (total-5 to end)
	olderEvents := events[:total-5]
	recentEvents := events[total-5:]

	// 5. Summarize older events
	olderText := formatEventsBlock(olderEvents)
	if summaryModel == "" {
		summaryModel = "dex-summary-model"
	}

	summaryPrompt := fmt.Sprintf("Summarize this conversation log concisely, retaining key details and user intent:\n\n%s", olderText)

	// We use Generate directly here.
	// Note: We are not handling the 'images' param for summary, assuming text-only context for now.
	summary, err := ollamaClient.Generate(summaryModel, summaryPrompt, nil)
	if err != nil {
		// Fallback: return full text if summary fails
		return formatEventsBlock(events), nil
	}

	// 6. Combine
	recentText := formatEventsBlock(recentEvents)

	finalContext := fmt.Sprintf("Summary of previous conversation:\n%s\n\nRecent messages:\n%s", strings.TrimSpace(summary), recentText)
	return finalContext, nil
}

// formatEventsBlock formats a slice of events into the standard log format
func formatEventsBlock(events []types.Event) string {
	var sb strings.Builder
	for _, evt := range events {
		var eventData map[string]interface{}
		// We explicitly ignore errors here as we want to skip malformed events
		if err := json.Unmarshal(evt.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)
			// Use templates to format
			line := templates.FormatEventAsText(eventType, eventData, evt.Service, evt.Timestamp, 0, "UTC", "en")
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}
