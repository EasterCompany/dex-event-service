package smartcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

// GetMessages fetches history and returns it as a slice of ollama.Message and their source EventIDs.
func GetMessages(ctx context.Context, redisClient *redis.Client, ollamaClient *ollama.Client, channelID string, summaryModel string) ([]ollama.Message, []string, error) {
	// 1. Fetch last 25 events
	eventIDs, err := redisClient.ZRevRange(ctx, "events:channel:"+channelID, 0, 24).Result()
	if err != nil {
		return nil, nil, err
	}

	if len(eventIDs) == 0 {
		return []ollama.Message{}, []string{}, nil
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
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	total := len(events)
	var messages []ollama.Message
	var contextEventIDs []string

	// Track all event IDs that are being included in this context window
	for _, e := range events {
		contextEventIDs = append(contextEventIDs, e.ID)
	}

	if total > 5 {
		// Summarize older 20
		olderEvents := events[:total-5]
		olderText := FormatEventsBlock(olderEvents)
		if summaryModel == "" {
			summaryModel = "dex-summary-model"
		}

		summaryPrompt := fmt.Sprintf("Summarize this conversation log concisely, retaining key details and user intent:\n\n%s", olderText)
		summary, _, err := ollamaClient.Generate(summaryModel, summaryPrompt, nil)
		if err == nil {
			messages = append(messages, ollama.Message{
				Role:    "system",
				Content: "Previous conversation summary: " + strings.TrimSpace(summary),
			})
			// Only process recent ones for raw message injection
			events = events[total-5:]
		}
	}

	// Add remaining events
	for _, evt := range events {
		var eventData map[string]interface{}
		if err := json.Unmarshal(evt.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)

			// Filter out engagement decisions as they are internal logs and shouldn't be in context
			if eventType == "engagement.decision" {
				continue
			}

			role := "system"
			content, _ := eventData["content"].(string)
			name := ""

			if eventType == string(types.EventTypeMessagingBotSentMessage) ||
				eventType == "messaging.bot.voice_response" {
				role = "assistant"
				name = "Dexter"
			} else if eventType == string(types.EventTypeMessagingUserSentMessage) {
				role = "user"
				name, _ = eventData["user_name"].(string)
			} else {
				content = cleanEventText(eventType, eventData, evt.Timestamp)
			}

			// Prefix user messages with their name for clarity,
			// but avoid prefixing assistant messages so Dexter doesn't mimic it.
			if role == "user" {
				content = fmt.Sprintf("[%s] %s", name, content)
			}

			messages = append(messages, ollama.Message{
				Role:    role,
				Content: content,
				Name:    name,
			})
		}
	}
	return messages, contextEventIDs, nil
}

// cleanEventText provides a human-friendly version of the event log
func cleanEventText(eventType string, data map[string]interface{}, ts int64) string {
	if eventType == "engagement.decision" {
		return ""
	}

	t := ""
	if ts > 0 {
		t = time.Unix(ts, 0).UTC().Format("15:04:05") + " | "
	}

	switch eventType {
	case string(types.EventTypeMessagingUserSentMessage):
		user, _ := data["user_name"].(string)
		content, _ := data["content"].(string)
		return fmt.Sprintf("%s%s: %s", t, user, content)
	case string(types.EventTypeMessagingBotSentMessage), "messaging.bot.voice_response":
		content, _ := data["content"].(string)
		return fmt.Sprintf("%s%s", t, content)
	default:
		// Fallback to standard but cleaner
		line := templates.FormatEventAsText(eventType, data, "", ts, 0, "UTC", "en")
		// Remove the service part if it exists (e.g., " | dex-discord-service | ")
		parts := strings.Split(line, " | ")
		if len(parts) >= 3 {
			return parts[0] + " | " + strings.Join(parts[2:], " | ")
		}
		return line
	}
}

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
		return FormatEventsBlock(events), nil
	}

	// Split: older (0 to total-5), recent (total-5 to end)
	olderEvents := events[:total-5]
	recentEvents := events[total-5:]

	// 5. Summarize older events
	olderText := FormatEventsBlock(olderEvents)
	if summaryModel == "" {
		summaryModel = "dex-summary-model"
	}

	summaryPrompt := fmt.Sprintf("Summarize this conversation log concisely, retaining key details and user intent:\n\n%s", olderText)

	// We use Generate directly here.
	// Note: We are not handling the 'images' param for summary, assuming text-only context for now.
	summary, _, err := ollamaClient.Generate(summaryModel, summaryPrompt, nil)
	if err != nil {
		// Fallback: return full text if summary fails
		return FormatEventsBlock(events), nil
	}
	// 6. Combine
	recentText := FormatEventsBlock(recentEvents)

	finalContext := fmt.Sprintf("Summary of previous conversation:\n%s\n\nRecent messages:\n%s", strings.TrimSpace(summary), recentText)
	return finalContext, nil
}

// FormatEventsBlock formats a slice of events into the standard log format
func FormatEventsBlock(events []types.Event) string {
	var sb strings.Builder
	for _, evt := range events {
		var eventData map[string]interface{}
		// We explicitly ignore errors here as we want to skip malformed events
		if err := json.Unmarshal(evt.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)

			if eventType == "engagement.decision" {
				continue
			}

			// Use templates to format
			line := templates.FormatEventAsText(eventType, eventData, evt.Service, evt.Timestamp, 0, "UTC", "en")
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}
