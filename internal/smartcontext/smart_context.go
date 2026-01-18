package smartcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/templates"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

const (
	SummaryContextLimitChars = 6000 // Approx 1500 tokens - Trigger summary update
	MaxRawEvents             = 50   // How many raw events to fetch to stitch with summary
)

type CachedSummary struct {
	Text        string `json:"text"`
	LastEventID string `json:"last_event_id"`
	Timestamp   int64  `json:"timestamp"` // Timestamp of the last event included in summary
}

// GetMessages fetches history using a hybrid "Lazy Summary" approach.
// It combines a cached long-term summary with recent raw messages.
// If the raw buffer grows too large, it triggers a background summarization.
func GetMessages(ctx context.Context, redisClient *redis.Client, ollamaClient *ollama.Client, channelID string, summaryModel string) ([]ollama.Message, []string, error) {
	// 1. Fetch Cached Summary
	var summary CachedSummary
	summaryKey := "context:summary:" + channelID
	val, err := redisClient.Get(ctx, summaryKey).Result()
	if err == nil {
		_ = json.Unmarshal([]byte(val), &summary)
	}

	// 2. Fetch Recent Events
	// We fetch a generous amount (MaxRawEvents) to ensure we overlap with the summary
	eventIDs, err := redisClient.ZRevRange(ctx, "events:channel:"+channelID, 0, int64(MaxRawEvents-1)).Result()
	if err != nil {
		return nil, nil, err
	}

	if len(eventIDs) == 0 {
		return []ollama.Message{}, []string{}, nil
	}

	// 3. Resolve Events
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

	// 4. Reorder to Chronological (Oldest -> Newest)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	// 5. Filter: Keep only events NEWER than the summary
	var activeContextEvents []types.Event
	var rawBufferTextLen int

	if summary.LastEventID == "" {
		// No summary? Use all events.
		activeContextEvents = events
		for _, e := range events {
			rawBufferTextLen += len(e.Event)
		}
	} else {
		// Find the cutoff point

		// Check if LastEventID is present in the list
		present := false
		for _, evt := range events {
			if evt.ID == summary.LastEventID {
				present = true
				break
			}
		}

		if !present {
			// Gap detected or summary is very old.
			// We treat the summary as "general background" and use all 50 raw events.
			activeContextEvents = events
			rawBufferTextLen = 0
			for _, e := range events {
				rawBufferTextLen += len(e.Event)
			}
		} else {
			// Overlap detected. Filter duplicates.
			appending := false
			for _, evt := range events {
				if appending {
					activeContextEvents = append(activeContextEvents, evt)
					rawBufferTextLen += len(evt.Event)
				} else if evt.ID == summary.LastEventID {
					appending = true
				}
			}
		}
	}

	// 6. Trigger Background Summarization if Buffer is Full
	// We check if raw buffer is large AND we aren't already summarizing
	if rawBufferTextLen > SummaryContextLimitChars {
		// Trigger background update using the raw events we have
		go func() {
			updateSummaryBackground(context.Background(), redisClient, ollamaClient, channelID, summaryModel, summary, events)
		}()
	}

	// 7. Build Message List
	var messages []ollama.Message
	var contextEventIDs []string

	// Inject Summary System Message
	if summary.Text != "" {
		messages = append(messages, ollama.Message{
			Role:    "system",
			Content: fmt.Sprintf("CONTEXT SUMMARY (Conversation so far):\n%s", summary.Text),
		})
	}

	// Inject Raw Messages
	for _, evt := range activeContextEvents {
		contextEventIDs = append(contextEventIDs, evt.ID)

		var eventData map[string]interface{}
		if err := json.Unmarshal(evt.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)
			if eventType == "engagement.decision" {
				continue
			}

			role := "system"
			content, _ := eventData["content"].(string)
			name := ""

			if eventType == string(types.EventTypeMessagingBotSentMessage) || eventType == "messaging.bot.voice_response" {
				role = "assistant"
				name = "Dexter"
			} else if eventType == string(types.EventTypeMessagingUserSentMessage) {
				role = "user"
				name, _ = eventData["user_name"].(string)
			} else {
				content = cleanEventText(eventType, eventData, evt.Timestamp)
			}

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

// Get fetches context for non-chat scenarios (returns string)
func Get(ctx context.Context, redisClient *redis.Client, ollamaClient *ollama.Client, channelID string, summaryModel string) (string, error) {
	msgs, _, err := GetMessages(ctx, redisClient, ollamaClient, channelID, summaryModel)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, msg := range msgs {
		role := strings.ToUpper(msg.Role)
		if msg.Name != "" {
			role = strings.ToUpper(msg.Name)
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n\n", role, msg.Content))
	}
	return sb.String(), nil
}

// updateSummaryBackground performs the summarization task
func updateSummaryBackground(ctx context.Context, rdb *redis.Client, client *ollama.Client, channelID string, model string, currentSummary CachedSummary, newEvents []types.Event) {
	// 1. Acquire Lock (debounce)
	lockKey := "context:summary:lock:" + channelID
	locked, err := rdb.SetNX(ctx, lockKey, "1", 30*time.Second).Result()
	if err != nil || !locked {
		return // Already summarizing
	}
	defer rdb.Del(ctx, lockKey)

	// 2. Prepare Data
	if len(newEvents) < 5 {
		return // Not enough data
	}

	// We want to summarize the *older* portion of the events.
	// We summarize everything except the last 3 messages to keep them fresh in the raw buffer for next time.
	eventsToSummarize := newEvents[:len(newEvents)-3]
	lastEvent := eventsToSummarize[len(eventsToSummarize)-1]

	// Check if this lastEvent is actually newer than what we already have
	if currentSummary.LastEventID == lastEvent.ID {
		return // Nothing new to summarize
	}

	textBlock := FormatEventsBlock(eventsToSummarize)

	var prompt string
	if currentSummary.Text != "" {
		prompt = fmt.Sprintf("Update the following conversation summary with the new events.\n\nEXISTING SUMMARY:\n%s\n\nNEW EVENTS:\n%s\n\nProvide a consolidated, concise summary of the ENTIRE conversation flow.", currentSummary.Text, textBlock)
	} else {
		prompt = fmt.Sprintf("Summarize this conversation log concisely, retaining key details and user intent:\n\n%s", textBlock)
	}

	if model == "" {
		model = "dex-fast-summary-model"
	}

	// 3. Generate
	newText, _, err := client.Generate(model, prompt, nil)
	if err != nil {
		log.Printf("Failed to update context summary for %s: %v", channelID, err)
		return
	}

	// 4. Save
	newSummary := CachedSummary{
		Text:        strings.TrimSpace(newText),
		LastEventID: lastEvent.ID,
		Timestamp:   lastEvent.Timestamp,
	}

	bytes, _ := json.Marshal(newSummary)
	rdb.Set(ctx, "context:summary:"+channelID, bytes, 24*time.Hour)

	log.Printf("Updated context summary for channel %s (Head: %s)", channelID, lastEvent.ID)
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
		parts := strings.Split(line, " | ")
		if len(parts) >= 3 {
			return parts[0] + " | " + strings.Join(parts[2:], " | ")
		}
		return line
	}
}

// FormatEventsBlock formats a slice of events into the standard log format
func FormatEventsBlock(events []types.Event) string {
	var sb strings.Builder
	for _, evt := range events {
		var eventData map[string]interface{}
		if err := json.Unmarshal(evt.Event, &eventData); err == nil {
			eventType, _ := eventData["type"].(string)
			if eventType == "engagement.decision" {
				continue
			}
			line := templates.FormatEventAsText(eventType, eventData, evt.Service, evt.Timestamp, 0, "UTC", "en")
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}
