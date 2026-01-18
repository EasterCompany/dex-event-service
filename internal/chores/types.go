package chores

// ChoreStatus represents the current state of a chore
type ChoreStatus string

const (
	ChoreStatusActive ChoreStatus = "active"
	ChoreStatusPaused ChoreStatus = "paused"
)

// ChoreExecutionPlan defines how the AI should execute the chore
type ChoreExecutionPlan struct {
	EntryURL        string `json:"entry_url"`
	SearchQuery     string `json:"search_query,omitempty"`
	ExtractionFocus string `json:"extraction_focus"` // Instructions for what to look for
}

// Chore represents a recurring task for the Courier Protocol
type Chore struct {
	ID                 string             `json:"id"`
	Recipients         []string           `json:"recipients"`
	Status             ChoreStatus        `json:"status"`
	Schedule           string             `json:"schedule"` // e.g., "every_6h", "daily"
	RunAt              string             `json:"run_at"`   // e.g., "08:00"
	Timezone           string             `json:"timezone"` // e.g., "Europe/London"
	LastRun            int64              `json:"last_run"`
	NaturalInstruction string             `json:"natural_instruction"`
	ExecutionPlan      ChoreExecutionPlan `json:"execution_plan"`
	Memory             []string           `json:"memory"` // List of IDs/Checksums of items already seen
	CreatedAt          int64              `json:"created_at"`
	UpdatedAt          int64              `json:"updated_at"`
}

// CreateChoreRequest is the payload for creating a new chore
type CreateChoreRequest struct {
	Recipients         []string `json:"recipients"`
	NaturalInstruction string   `json:"natural_instruction"`
	Schedule           string   `json:"schedule"` // Defaults to "every_6h" if empty
	RunAt              string   `json:"run_at"`   // Optional: "HH:MM"
	Timezone           string   `json:"timezone"` // Optional: "Europe/London"
	// Optional: Users/AI can provide pre-filled plan
	EntryURL        string `json:"entry_url,omitempty"`
	SearchQuery     string `json:"search_query,omitempty"`
	ExtractionFocus string `json:"extraction_focus,omitempty"`
}

// UpdateChoreRequest allows partial updates
type UpdateChoreRequest struct {
	Status             *ChoreStatus `json:"status,omitempty"`
	Schedule           *string      `json:"schedule,omitempty"`
	RunAt              *string      `json:"run_at,omitempty"`
	Timezone           *string      `json:"timezone,omitempty"`
	NaturalInstruction *string      `json:"natural_instruction,omitempty"`
	Recipients         []string     `json:"recipients,omitempty"`
	Memory             []string     `json:"memory,omitempty"` // Replaces memory if provided
	LastRun            *int64       `json:"last_run,omitempty"`
}
