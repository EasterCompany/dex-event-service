package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const roadmapKeyPrefix = "roadmap:"

// RoadmapHandler routes roadmap-related requests.
func RoadmapHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/roadmap")
		path = strings.TrimPrefix(path, "/")

		switch r.Method {
		case http.MethodGet:
			if path == "" {
				ListRoadmapHandler(redisClient)(w, r)
			} else {
				GetRoadmapItemHandler(redisClient, path)(w, r)
			}
		case http.MethodPost:
			CreateRoadmapItemHandler(redisClient)(w, r)
		case http.MethodPatch:
			if path != "" {
				UpdateRoadmapItemHandler(redisClient, path)(w, r)
			} else {
				http.Error(w, "Item ID required", http.StatusBadRequest)
			}
		case http.MethodDelete:
			if path != "" {
				DeleteRoadmapItemHandler(redisClient, path)(w, r)
			} else {
				http.Error(w, "Item ID required", http.StatusBadRequest)
			}
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ListRoadmapHandler retrieves all roadmap items.
func ListRoadmapHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		keys, err := redisClient.Keys(ctx, roadmapKeyPrefix+"*").Result()
		if err != nil {
			http.Error(w, "Failed to query items", http.StatusInternalServerError)
			return
		}

		items := make([]types.RoadmapItem, 0)
		for _, key := range keys {
			data, err := redisClient.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var item types.RoadmapItem
			if err := json.Unmarshal([]byte(data), &item); err == nil {
				items = append(items, item)
			}
		}

		// Sort by CreatedAt (descending)
		sort.Slice(items, func(i, j int) bool {
			return items[i].CreatedAt > items[j].CreatedAt
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	}
}

// CreateRoadmapItemHandler creates a new Draft roadmap item.
func CreateRoadmapItemHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateRoadmapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		now := time.Now().Unix()
		item := types.RoadmapItem{
			ID:        uuid.New().String(),
			Content:   req.Content,
			State:     types.RoadmapStateDraft,
			CreatedAt: now,
			UpdatedAt: now,
		}

		data, _ := json.Marshal(item)
		ctx := context.Background()
		if err := redisClient.Set(ctx, roadmapKeyPrefix+item.ID, data, utils.DefaultTTL).Err(); err != nil {
			http.Error(w, "Failed to save item", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(item)

		// Emit Event
		utils.SendEvent(ctx, redisClient, "roadmap", "system.roadmap.created", map[string]interface{}{
			"id":      item.ID,
			"content": item.Content,
			"state":   item.State,
		})
	}
}

// UpdateRoadmapItemHandler updates an item's content or state.
func UpdateRoadmapItemHandler(redisClient *redis.Client, id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		key := roadmapKeyPrefix + id

		data, err := redisClient.Get(ctx, key).Result()
		if err == redis.Nil {
			http.Error(w, "Item not found", http.StatusNotFound)
			return
		}

		var item types.RoadmapItem
		_ = json.Unmarshal([]byte(data), &item)

		var req types.UpdateRoadmapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Content != nil {
			item.Content = *req.Content
		}
		if req.State != nil {
			item.State = *req.State
		}
		item.UpdatedAt = time.Now().Unix()

		updatedData, _ := json.Marshal(item)
		if err := redisClient.Set(ctx, key, updatedData, utils.DefaultTTL).Err(); err != nil {
			http.Error(w, "Failed to update item", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)

		// Emit Event
		utils.SendEvent(ctx, redisClient, "roadmap", "system.roadmap.updated", map[string]interface{}{
			"id":    item.ID,
			"state": item.State,
		})
	}
}

// GetRoadmapItemHandler retrieves a single item.
func GetRoadmapItemHandler(redisClient *redis.Client, id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		data, err := redisClient.Get(ctx, roadmapKeyPrefix+id).Result()
		if err == redis.Nil {
			http.Error(w, "Item not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(data))
	}
}

// DeleteRoadmapItemHandler removes an item.
func DeleteRoadmapItemHandler(redisClient *redis.Client, id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		if err := redisClient.Del(ctx, roadmapKeyPrefix+id).Err(); err != nil {
			http.Error(w, "Failed to delete item", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
