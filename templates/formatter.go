package templates

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FormatEventAsText converts an event to a human-readable text line
func FormatEventAsText(eventType string, eventData map[string]interface{}, service string, timestamp int64, depth int, timezone string, language string) string {
	templates := GetTemplates()
	template, exists := templates[eventType]

	// Format timestamp with timezone
	timeStr := formatTimestamp(timestamp, timezone)

	// Build indentation for parent-child relationships
	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "└─ "
	}

	// Build formatted message
	var message string
	if exists {
		// Get the best format string for the requested language
		formatStr := GetFormatString(template, language)
		if formatStr != "" {
			// Use format string with language fallback
			message = interpolateFormat(formatStr, eventData)
		} else {
			// Fall back to generic formatting
			message = formatGeneric(eventType, eventData)
		}
	} else {
		// Fall back to generic formatting
		message = formatGeneric(eventType, eventData)
	}

	// Combine all parts
	return fmt.Sprintf("%s%s | %s | %s", prefix, timeStr, service, message)
}

// interpolateFormat replaces {field} placeholders with actual values
func interpolateFormat(format string, eventData map[string]interface{}) string {
	result := format

	// Find all {field} placeholders and replace them
	for key, value := range eventData {
		if key == "type" {
			continue // Skip the type field
		}

		placeholder := "{" + key + "}"
		valueStr := formatValue(value)
		result = strings.ReplaceAll(result, placeholder, valueStr)
	}

	// Clean up any remaining unreplaced placeholders
	result = strings.ReplaceAll(result, "{", "")
	result = strings.ReplaceAll(result, "}", "")

	return result
}

// formatValue converts a value to a string for display
func formatValue(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case float64:
		// Check if it's actually an integer
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%.2f", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case map[string]interface{}:
		// For objects, show as JSON
		jsonBytes, _ := json.Marshal(v)
		return string(jsonBytes)
	case []interface{}:
		// For arrays, show count or contents
		if len(v) == 0 {
			return "[]"
		}
		if len(v) <= 3 {
			jsonBytes, _ := json.Marshal(v)
			return string(jsonBytes)
		}
		return fmt.Sprintf("[%d items]", len(v))
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatGeneric creates a generic text representation when no format string exists
func formatGeneric(eventType string, eventData map[string]interface{}) string {
	lines := []string{eventType}

	for key, value := range eventData {
		if key == "type" {
			continue
		}

		// Don't show complex objects inline in generic format
		valueStr := formatValue(value)
		if len(valueStr) > 100 {
			valueStr = valueStr[:97] + "..."
		}

		lines = append(lines, fmt.Sprintf("  %s: %s", key, valueStr))
	}

	return strings.Join(lines, "\n")
}

// formatTimestamp converts a Unix timestamp to a formatted string in the specified timezone
func formatTimestamp(timestamp int64, timezone string) string {
	t := time.Unix(timestamp, 0)

	// Default to UTC if no timezone specified
	if timezone == "" {
		timezone = "UTC"
	}

	// Load the timezone location
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		// If timezone is invalid, fall back to UTC
		loc = time.UTC
	}

	// Convert to requested timezone and format
	localTime := t.In(loc)

	// Format: 2006-01-02 15:04:05 MST
	return localTime.Format("2006-01-02 15:04:05 MST")
}
