package chores

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	KeyPrefix        = "chores:data:"
	IndexAll         = "chores:index:all"
	IndexOwnerPrefix = "chores:index:owner:"
)

type Store struct {
	client *redis.Client
}

func NewStore(client *redis.Client) *Store {
	return &Store{client: client}
}

// Create adds a new chore to Redis
func (s *Store) Create(ctx context.Context, req CreateChoreRequest) (*Chore, error) {
	id := uuid.New().String()
	now := time.Now().Unix()

	if req.Schedule == "" {
		req.Schedule = "every_6h"
	}

	// Ensure recipients includes owner if empty
	recipients := req.Recipients
	if len(recipients) == 0 && req.OwnerID != "" {
		recipients = []string{req.OwnerID}
	}

	chore := &Chore{
		ID:                 id,
		OwnerID:            req.OwnerID,
		Recipients:         recipients,
		Status:             ChoreStatusActive,
		Schedule:           req.Schedule,
		LastRun:            0,
		NaturalInstruction: req.NaturalInstruction,
		ExecutionPlan: ChoreExecutionPlan{
			EntryURL:        req.EntryURL,
			SearchQuery:     req.SearchQuery,
			ExtractionFocus: req.ExtractionFocus,
		},
		Memory:    []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(chore)
	if err != nil {
		return nil, err
	}

	pipe := s.client.Pipeline()
	pipe.Set(ctx, KeyPrefix+id, data, 0)
	pipe.SAdd(ctx, IndexAll, id)
	if req.OwnerID != "" {
		pipe.SAdd(ctx, IndexOwnerPrefix+req.OwnerID, id)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	return chore, nil
}

// Get retrieves a chore by ID
func (s *Store) Get(ctx context.Context, id string) (*Chore, error) {
	data, err := s.client.Get(ctx, KeyPrefix+id).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("chore not found")
		}
		return nil, err
	}

	var chore Chore
	if err := json.Unmarshal(data, &chore); err != nil {
		return nil, err
	}
	return &chore, nil
}

// GetAll retrieves all chores
func (s *Store) GetAll(ctx context.Context) ([]*Chore, error) {
	ids, err := s.client.SMembers(ctx, IndexAll).Result()
	if err != nil {
		return nil, err
	}

	var chores []*Chore
	for _, id := range ids {
		c, err := s.Get(ctx, id)
		if err == nil {
			chores = append(chores, c)
		}
	}
	return chores, nil
}

// GetByOwner retrieves all chores for a specific owner
func (s *Store) GetByOwner(ctx context.Context, ownerID string) ([]*Chore, error) {
	ids, err := s.client.SMembers(ctx, IndexOwnerPrefix+ownerID).Result()
	if err != nil {
		return nil, err
	}

	var chores []*Chore
	for _, id := range ids {
		c, err := s.Get(ctx, id)
		if err == nil {
			chores = append(chores, c)
		}
	}
	return chores, nil
}

// Update modifies an existing chore
func (s *Store) Update(ctx context.Context, id string, req UpdateChoreRequest) (*Chore, error) {
	chore, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Status != nil {
		chore.Status = *req.Status
	}
	if req.Schedule != nil {
		chore.Schedule = *req.Schedule
	}
	if req.NaturalInstruction != nil {
		chore.NaturalInstruction = *req.NaturalInstruction
	}
	if req.Recipients != nil {
		chore.Recipients = req.Recipients
	}
	if req.Memory != nil {
		chore.Memory = req.Memory
	}

	chore.UpdatedAt = time.Now().Unix()

	data, err := json.Marshal(chore)
	if err != nil {
		return nil, err
	}

	if err := s.client.Set(ctx, KeyPrefix+id, data, 0).Err(); err != nil {
		return nil, err
	}

	return chore, nil
}

// MarkRun updates the last run timestamp and optionally memory
func (s *Store) MarkRun(ctx context.Context, id string, newMemory []string) error {
	chore, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	chore.LastRun = time.Now().Unix()
	if newMemory != nil {
		chore.Memory = newMemory
	}

	data, err := json.Marshal(chore)
	if err != nil {
		return err
	}

	return s.client.Set(ctx, KeyPrefix+id, data, 0).Err()
}

// Delete removes a chore
func (s *Store) Delete(ctx context.Context, id string) error {
	chore, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	pipe := s.client.Pipeline()
	pipe.Del(ctx, KeyPrefix+id)
	pipe.SRem(ctx, IndexAll, id)
	if chore.OwnerID != "" {
		pipe.SRem(ctx, IndexOwnerPrefix+chore.OwnerID, id)
	}

	_, err = pipe.Exec(ctx)
	return err
}
