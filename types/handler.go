package types

// HandlerConfig defines a handler that can process events
type HandlerConfig struct {
	Name        string   `json:"name"`                   // Handler identifier
	Binary      string   `json:"binary"`                 // Path to binary (relative to ~/Dexter/bin/)
	Description string   `json:"description,omitempty"`  // What this handler does
	Timeout     int      `json:"timeout,omitempty"`      // Timeout in seconds (default: 30)
	DebounceKey string   `json:"debounce_key,omitempty"` // Key to extract from event data for single-task execution (e.g. "channel_id")
	EventTypes  []string `json:"event_types,omitempty"`  // Event types this handler applies to (if empty, must be specified per-event)
	OutputEvent string   `json:"output_event,omitempty"` // Event type for child events (if empty, handler determines it)
}

// HandlerRegistry holds all configured handlers
type HandlerRegistry struct {
	Handlers map[string]HandlerConfig `json:"handlers"` // Map of handler name -> config
}

// HandlerInput is the JSON structure passed to handler binaries via stdin
type HandlerInput struct {
	EventID   string                 `json:"event_id"`   // ID of the parent event
	Service   string                 `json:"service"`    // Service that created the event
	EventType string                 `json:"event_type"` // Type of event (from event.type field)
	EventData map[string]interface{} `json:"event_data"` // Full event data
	Timestamp int64                  `json:"timestamp"`  // Event timestamp
}

// HandlerOutput is the expected JSON structure from handler binaries via stdout
type HandlerOutput struct {
	Success bool                 `json:"success"`          // Whether handler succeeded
	Error   string               `json:"error,omitempty"`  // Error message if failed
	Events  []HandlerOutputEvent `json:"events,omitempty"` // Child events to create
}

// HandlerOutputEvent represents a child event to be created
type HandlerOutputEvent struct {
	Type string                 `json:"type"` // Event type (must be valid template)
	Data map[string]interface{} `json:"data"` // Event data (will be validated against template)
}
