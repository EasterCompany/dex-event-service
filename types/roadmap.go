package types

// RoadmapState represents the lifecycle state of a roadmap item.
type RoadmapState string

const (
	RoadmapStateDraft     RoadmapState = "draft"
	RoadmapStatePublished RoadmapState = "published"
	RoadmapStateConsumed  RoadmapState = "consumed"
)

// RoadmapItem represents a strategic objective or feature idea from the creator.
type RoadmapItem struct {
	ID         string       `json:"id"`
	Content    string       `json:"content"`
	State      RoadmapState `json:"state"`
	CreatedAt  int64        `json:"created_at"`
	UpdatedAt  int64        `json:"updated_at"`
	ConsumedAt int64        `json:"consumed_at,omitempty"`
	ResultID   string       `json:"result_id,omitempty"`   // ID of the generated blueprint/notification
	ResultType string       `json:"result_type,omitempty"` // "blueprint" or "notification"
}

// CreateRoadmapRequest is the payload for creating a new roadmap item.
type CreateRoadmapRequest struct {
	Content string `json:"content"`
}

// UpdateRoadmapRequest is the payload for updating an existing roadmap item.
type UpdateRoadmapRequest struct {
	Content *string       `json:"content,omitempty"`
	State   *RoadmapState `json:"state,omitempty"`
}
