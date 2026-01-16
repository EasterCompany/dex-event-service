package templates

import (
	"fmt"
)

// Validate checks if an event matches its template
// Returns a list of validation errors, or empty slice if valid
func Validate(eventType string, eventData map[string]interface{}) []ValidationError {
	templates := GetTemplates()
	template, exists := templates[eventType]

	if !exists {
		return []ValidationError{{
			Field:   "type",
			Message: fmt.Sprintf("unknown event type '%s' - must be one of: %v", eventType, GetTemplateList()),
		}}
	}

	var errors []ValidationError

	// Check required fields are present
	for fieldName, spec := range template.Fields {
		if spec.Required {
			if _, exists := eventData[fieldName]; !exists {
				errors = append(errors, ValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("required field '%s' is missing", fieldName),
				})
			}
		}
	}

	// Check field types
	for fieldName, value := range eventData {
		spec, fieldDefined := template.Fields[fieldName]

		// Allow extra fields not in template (for flexibility)
		if !fieldDefined {
			continue
		}

		// Validate type
		if !isValidType(value, spec.Type) {
			errors = append(errors, ValidationError{
				Field:   fieldName,
				Message: fmt.Sprintf("field '%s' must be of type %s, got %T", fieldName, spec.Type, value),
			})
		}
	}

	return errors
}

// isValidType checks if a value matches the expected type
func isValidType(value interface{}, expectedType string) bool {
	if value == nil {
		return true // Allow null for optional fields
	}

	switch expectedType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		// JSON numbers come as float64
		_, okFloat := value.(float64)
		_, okInt := value.(int)
		return okFloat || okInt
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "array":
		// Check if it's a slice of any type
		switch value.(type) {
		case []interface{}, []string, []int, []float64, []bool:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// GetTemplateList returns a list of all available event types
func GetTemplateList() []string {
	templates := GetTemplates()
	types := make([]string, 0, len(templates))
	for typeName := range templates {
		types = append(types, typeName)
	}
	return types
}
