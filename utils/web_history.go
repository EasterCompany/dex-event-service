package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/web"
	"github.com/redis/go-redis/v9"
)

type WebHistoryItem struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Timestamp  int64  `json:"timestamp"`
	Screenshot string `json:"screenshot"` // Base64
	Content    string `json:"content"`    // Full content or snippet
}

const WebHistoryKey = "system:web:history"
const WebHistoryLimit = 10

func StoreWebHistory(redis *redis.Client, resp *web.WebViewResponse, url string) error {
	if redis == nil {
		return fmt.Errorf("redis client is nil")
	}

	// Truncate content to save space if needed
	content := resp.Content
	if len(content) > 50000 {
		content = content[:50000] + "...(truncated)"
	}

	item := WebHistoryItem{
		URL:        url,
		Title:      resp.Title,
		Timestamp:  time.Now().Unix(),
		Screenshot: resp.Screenshot,
		Content:    content,
	}

	return StoreWebHistoryItem(redis, item)
}

func StoreWebHistoryItem(redis *redis.Client, item WebHistoryItem) error {
	if redis == nil {
		return fmt.Errorf("redis client is nil")
	}

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	ctx := context.Background()
	pipe := redis.Pipeline()
	pipe.LPush(ctx, WebHistoryKey, data)
	pipe.LTrim(ctx, WebHistoryKey, 0, WebHistoryLimit-1)
	_, err = pipe.Exec(ctx)
	return err
}

func GetWebHistory(redis *redis.Client) ([]WebHistoryItem, error) {
	if redis == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	ctx := context.Background()
	data, err := redis.LRange(ctx, WebHistoryKey, 0, WebHistoryLimit-1).Result()
	if err != nil {
		return nil, err
	}

	var history []WebHistoryItem
	for _, d := range data {
		var item WebHistoryItem
		if err := json.Unmarshal([]byte(d), &item); err == nil {
			history = append(history, item)
		}
	}
	return history, nil
}
