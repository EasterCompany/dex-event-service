package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
	"github.com/redis/go-redis/v9"
)

const (
	SyncQueueKey   = "sync:queue"
	EventKeyPrefix = "event:"
	TimelineKey    = "events:timeline"
)

// Manager handles dual-write storage to Cloud (Primary) and Local (Fallback) Redis.
// It splits cloud responsibilities: EventCloud for timeline, StateCloud for cache/locks.
type Manager struct {
	EventClient *redis.Client // cloud-cache-0
	StateClient *redis.Client // cloud-cache-1
	LocalClient *redis.Client // local-cache-0 (Fallback for both)
	mu          sync.RWMutex
	eventOnline bool
	stateOnline bool
}

// NewManager creates a new Storage Manager.
func NewManager(eventCloud, stateCloud, local *redis.Client) *Manager {
	m := &Manager{
		EventClient: eventCloud,
		StateClient: stateCloud,
		LocalClient: local,
		eventOnline: true,
		stateOnline: true,
	}
	go m.monitorCloudHealth()
	go m.syncWorker()
	return m
}

// monitorCloudHealth periodically pings the cloud Redis instances.
func (m *Manager) monitorCloudHealth() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		// Check Event Cloud
		errEvent := m.EventClient.Ping(ctx).Err()

		// Check State Cloud
		errState := m.StateClient.Ping(ctx).Err()

		cancel()

		m.mu.Lock()
		// Update Event Cloud Status
		if errEvent != nil {
			if m.eventOnline {
				log.Printf("Storage: Event Cloud Redis is OFFLINE: %v", errEvent)
			}
			m.eventOnline = false
		} else {
			if !m.eventOnline {
				log.Printf("Storage: Event Cloud Redis is ONLINE")
			}
			m.eventOnline = true
		}

		// Update State Cloud Status
		if errState != nil {
			if m.stateOnline {
				log.Printf("Storage: State Cloud Redis is OFFLINE: %v", errState)
			}
			m.stateOnline = false
		} else {
			if !m.stateOnline {
				log.Printf("Storage: State Cloud Redis is ONLINE")
			}
			m.stateOnline = true
		}
		m.mu.Unlock()
	}
}

// IsEventCloudOnline returns status of event storage.
func (m *Manager) IsEventCloudOnline() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eventOnline
}

// IsStateCloudOnline returns status of state storage.
func (m *Manager) IsStateCloudOnline() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateOnline
}

// SaveEvent writes an event to storage using the resilient strategy.
func (m *Manager) SaveEvent(ctx context.Context, event *types.Event) error {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// 1. Try Event Cloud Write
	if m.IsEventCloudOnline() {
		if err := m.writeToClient(ctx, m.EventClient, event, eventJSON); err != nil {
			log.Printf("Storage: Write to Event Cloud failed: %v. Fallback to Local.", err)
			m.mu.Lock()
			m.eventOnline = false
			m.mu.Unlock()
		} else {
			// Cloud Success -> Mirror to Local
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.writeToClient(bgCtx, m.LocalClient, event, eventJSON); err != nil {
					log.Printf("Storage: Mirror to Local failed: %v", err)
				}
			}()
			return nil
		}
	}

	// 2. Fallback: Write to Local AND Add to Sync Queue
	if err := m.writeToClient(ctx, m.LocalClient, event, eventJSON); err != nil {
		return fmt.Errorf("CRITICAL: Write to Local failed: %w", err)
	}

	// Mark for sync
	if err := m.LocalClient.SAdd(ctx, SyncQueueKey, event.ID).Err(); err != nil {
		log.Printf("Storage: Failed to add event %s to sync queue: %v", event.ID, err)
	}

	return nil
}

// writeToClient performs the actual Redis commands for a single client.
func (m *Manager) writeToClient(ctx context.Context, client *redis.Client, event *types.Event, eventJSON []byte) error {
	pipe := client.Pipeline()

	// Store event data
	eventKey := EventKeyPrefix + event.ID
	pipe.Set(ctx, eventKey, eventJSON, utils.DefaultTTL)

	// Add to global timeline
	pipe.ZAdd(ctx, TimelineKey, redis.Z{
		Score:  float64(event.Timestamp),
		Member: event.ID,
	})

	// Add to service timeline
	serviceTimelineKey := fmt.Sprintf("events:service:%s", event.Service)
	pipe.ZAdd(ctx, serviceTimelineKey, redis.Z{
		Score:  float64(event.Timestamp),
		Member: event.ID,
	})

	// Add to channel timeline if applicable
	var eventData map[string]interface{}
	if err := json.Unmarshal(event.Event, &eventData); err == nil {
		var channelID string
		if cid, ok := eventData["channel_id"].(string); ok {
			channelID = cid
		} else if tid, ok := eventData["target_channel"].(string); ok {
			channelID = tid
		}

		if channelID != "" {
			channelTimelineKey := fmt.Sprintf("events:channel:%s", channelID)
			pipe.ZAdd(ctx, channelTimelineKey, redis.Z{
				Score:  float64(event.Timestamp),
				Member: event.ID,
			})
		}
	}

	_, err := pipe.Exec(ctx)
	return err
}

// GetEvent retrieves an event by ID (Event Cloud preferred, Local fallback).
func (m *Manager) GetEvent(ctx context.Context, eventID string) (*types.Event, error) {
	if m.IsEventCloudOnline() {
		event, err := m.getFromClient(ctx, m.EventClient, eventID)
		if err == nil {
			return event, nil
		}
		if err != redis.Nil {
			log.Printf("Storage: Event Cloud Read Error: %v", err)
		}
	}
	return m.getFromClient(ctx, m.LocalClient, eventID)
}

func (m *Manager) getFromClient(ctx context.Context, client *redis.Client, eventID string) (*types.Event, error) {
	val, err := client.Get(ctx, EventKeyPrefix+eventID).Result()
	if err != nil {
		return nil, err
	}
	var event types.Event
	if err := json.Unmarshal([]byte(val), &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// GetTimeline retrieves a slice of events (Event Cloud preferred).
func (m *Manager) GetTimeline(ctx context.Context, key string, opt *redis.ZRangeBy) ([]string, error) {
	if m.IsEventCloudOnline() {
		res, err := m.EventClient.ZRevRangeByScore(ctx, key, opt).Result()
		if err == nil {
			return res, nil
		}
	}
	return m.LocalClient.ZRevRangeByScore(ctx, key, opt).Result()
}

// syncWorker processes the offline queue when Event Cloud is back online.
func (m *Manager) syncWorker() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		if !m.IsEventCloudOnline() {
			continue
		}

		ctx := context.Background()

		// Check queue size
		count, err := m.LocalClient.SCard(ctx, SyncQueueKey).Result()
		if err != nil || count == 0 {
			continue
		}

		log.Printf("Storage: Syncing %d events from Local to Event Cloud...", count)

		ids, err := m.LocalClient.SMembers(ctx, SyncQueueKey).Result()
		if err != nil {
			continue
		}

		syncedCount := 0
		for _, id := range ids {
			// Get from Local
			event, err := m.getFromClient(ctx, m.LocalClient, id)
			if err != nil {
				if err == redis.Nil {
					m.LocalClient.SRem(ctx, SyncQueueKey, id)
				}
				continue
			}

			eventJSON, _ := json.Marshal(event)

			// Write to Event Cloud
			if err := m.writeToClient(ctx, m.EventClient, event, eventJSON); err != nil {
				log.Printf("Storage: Failed to sync event %s to cloud: %v", id, err)
				continue
			}

			// Remove from Queue
			m.LocalClient.SRem(ctx, SyncQueueKey, id)
			syncedCount++
		}

		if syncedCount > 0 {
			log.Printf("Storage: Successfully synced %d events.", syncedCount)
		}
	}
}

// Proxy methods for generic Redis operations (Cache/Locks) -> State Cloud
func (m *Manager) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	if m.IsStateCloudOnline() {
		return m.StateClient.Set(ctx, key, value, expiration)
	}
	return m.LocalClient.Set(ctx, key, value, expiration)
}

func (m *Manager) Get(ctx context.Context, key string) *redis.StringCmd {
	if m.IsStateCloudOnline() {
		res := m.StateClient.Get(ctx, key)
		if res.Err() == nil || res.Err() == redis.Nil {
			return res
		}
	}
	return m.LocalClient.Get(ctx, key)
}

func (m *Manager) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	if m.IsStateCloudOnline() {
		return m.StateClient.Del(ctx, keys...)
	}
	return m.LocalClient.Del(ctx, keys...)
}

func (m *Manager) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd {
	if m.IsStateCloudOnline() {
		return m.StateClient.SetNX(ctx, key, value, expiration)
	}
	return m.LocalClient.SetNX(ctx, key, value, expiration)
}

func (m *Manager) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	if m.IsStateCloudOnline() {
		return m.StateClient.Expire(ctx, key, expiration)
	}
	return m.LocalClient.Expire(ctx, key, expiration)
}

func (m *Manager) Pipeline() redis.Pipeliner {
	// For general pipeline usage, default to State Cloud if online
	if m.IsStateCloudOnline() {
		return m.StateClient.Pipeline()
	}
	return m.LocalClient.Pipeline()
}

// ZRevRangeByScore and ZCount are usually for timeline (events), so use Event Cloud
func (m *Manager) ZRevRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) *redis.StringSliceCmd {
	if m.IsEventCloudOnline() {
		return m.EventClient.ZRevRangeByScore(ctx, key, opt)
	}
	return m.LocalClient.ZRevRangeByScore(ctx, key, opt)
}

func (m *Manager) ZCount(ctx context.Context, key, min, max string) *redis.IntCmd {
	if m.IsEventCloudOnline() {
		return m.EventClient.ZCount(ctx, key, min, max)
	}
	return m.LocalClient.ZCount(ctx, key, min, max)
}
