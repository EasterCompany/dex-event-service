package templates

// EventTemplate defines the structure and validation rules for an event type
type EventTemplate struct {
	Description string               `json:"description"`
	Format      string               `json:"format,omitempty"`  // Default/English format string with {field} placeholders
	Formats     map[string]string    `json:"formats,omitempty"` // Language-specific format strings (key: language code)
	Fields      map[string]FieldSpec `json:"fields"`
}

// FieldSpec defines the requirements for a single field
type FieldSpec struct {
	Type        string `json:"type"` // "string", "number", "boolean", "object", "array"
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// ValidationError represents a validation failure
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
