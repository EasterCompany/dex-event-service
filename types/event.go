package types

import (
	"encoding/json"
)

// Event represents a single event in the timeline
type Event struct {
	ID        string          `json:"id"`
	Service   string          `json:"service"`
	Event     json.RawMessage `json:"event"`
	Timestamp int64           `json:"timestamp"`
	ParentID  string          `json:"parent_id,omitempty"` // ID of parent event if this is a child
	ChildIDs  []string        `json:"child_ids,omitempty"` // IDs of child events spawned by handlers
}

// CreateEventRequest is the request body for creating an event
type CreateEventRequest struct {
	Service     string          `json:"service"`
	Event       json.RawMessage `json:"event"`
	Handler     string          `json:"handler,omitempty"`      // Optional: override/specify handler
	HandlerMode string          `json:"handler_mode,omitempty"` // "sync" or "async" (default: async)
}

// CreateEventResponse is the response for creating an event
type CreateEventResponse struct {
	ID       string   `json:"id"`                  // Parent event ID
	ChildIDs []string `json:"child_ids,omitempty"` // Child event IDs (only for sync handlers)
}

// GetTimelineRequest represents query parameters for the timeline endpoint
type GetTimelineRequest struct {
	MaxLength    int   `json:"max_length"`
	MinTimestamp int64 `json:"min_timestamp,omitempty"`
	MaxTimestamp int64 `json:"max_timestamp,omitempty"`
	Ascending    bool  `json:"ascending"` // false = last-to-first, true = first-to-last
}

// GetTimelineResponse is the response for the timeline endpoint
type GetTimelineResponse struct {
	Events []Event `json:"events"`
	Count  int     `json:"count"`
}
