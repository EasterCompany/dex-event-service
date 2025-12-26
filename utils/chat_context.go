package utils

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/redis/go-redis/v9"
)

const (
	// ChatHistoryKeyPrefix is the prefix for chat history keys in Redis
	ChatHistoryKeyPrefix = "analyst:memory:"

	// DefaultAttentionSpan is the default TTL for chat history (1 hour)
	DefaultAttentionSpan = 1 * time.Hour
)

// ChatContextManager handles the persistence of chat history.
type ChatContextManager struct {
	Redis *redis.Client
}

// NewChatContextManager creates a new manager.
func NewChatContextManager(r *redis.Client) *ChatContextManager {
	return &ChatContextManager{Redis: r}
}

// LoadHistory retrieves the chat history for a specific tier/session.
func (m *ChatContextManager) LoadHistory(ctx context.Context, sessionID string) ([]ollama.Message, error) {
	key := ChatHistoryKeyPrefix + sessionID
	data, err := m.Redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return []ollama.Message{}, nil
	}
	if err != nil {
		return nil, err
	}

	var history []ollama.Message
	if err := json.Unmarshal([]byte(data), &history); err != nil {
		return nil, err
	}
	return history, nil
}

// SaveHistory updates the chat history in Redis and resets the TTL ("Attention Span").
func (m *ChatContextManager) SaveHistory(ctx context.Context, sessionID string, history []ollama.Message) error {
	key := ChatHistoryKeyPrefix + sessionID
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}

	// Save with TTL (Sliding Expiration)
	return m.Redis.Set(ctx, key, data, DefaultAttentionSpan).Err()
}

// AppendMessage adds a single message to history and saves it.
func (m *ChatContextManager) AppendMessage(ctx context.Context, sessionID string, msg ollama.Message) error {
	history, err := m.LoadHistory(ctx, sessionID)
	if err != nil {
		return err
	}

	history = append(history, msg)

	// Limit history size to prevent context overflow (e.g., keep last 20 messages)
	// We keep the first message (System Prompt usually) and the last N
	if len(history) > 20 {
		// Preserve system prompt at index 0
		systemPrompt := history[0]
		// Keep last 19
		recent := history[len(history)-19:]
		history = append([]ollama.Message{systemPrompt}, recent...)
	}

	return m.SaveHistory(ctx, sessionID, history)
}

// ClearHistory wipes the memory for a session (used for hard resets).
func (m *ChatContextManager) ClearHistory(ctx context.Context, sessionID string) error {
	key := ChatHistoryKeyPrefix + sessionID
	return m.Redis.Del(ctx, key).Err()
}
