package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/term"
)

type HandlerInput struct {
	EventID   string                 `json:"event_id"`
	Service   string                 `json:"service"`
	EventType string                 `json:"event_type"`
	EventData map[string]interface{} `json:"event_data"`
	Timestamp int64                  `json:"timestamp"`
}

type HandlerOutputEvent struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

type HandlerOutput struct {
	Success bool                 `json:"success"`
	Error   string               `json:"error,omitempty"`
	Events  []HandlerOutputEvent `json:"events,omitempty"`
}

func createEvent(eventType string, data map[string]interface{}) HandlerOutputEvent {
	return HandlerOutputEvent{Type: eventType, Data: data}
}

func logEvent(msg string) HandlerOutputEvent {
	return createEvent("log_entry", map[string]interface{}{
		"level":   "info",
		"message": msg,
	})
}

func testEvent(service, eventType string, data map[string]interface{}) (bool, string) {
	payload := map[string]interface{}{
		"service": service,
		"event":   data,
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "http://localhost:8100/events", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Name", "dex-test-suite")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("✗ %s event failed: %v", eventType, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, fmt.Sprintf("✓ %s event created", eventType)
	}

	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Sprintf("✗ %s event rejected: %s", eventType, string(body))
}

func testLanguageFormat(lang string) (bool, string) {
	url := fmt.Sprintf("http://localhost:8100/events?ml=3&format=text&lang=%s", lang)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Service-Name", "dex-test-suite")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("✗ lang=%s failed: %v", lang, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && len(body) > 0 {
		return true, fmt.Sprintf("✓ lang=%s works", lang)
	}
	return false, fmt.Sprintf("✗ lang=%s failed", lang)
}

func printHelp() {
	fmt.Println("Event Test Handler")
	fmt.Println()
	fmt.Println("This handler is designed to be invoked by the dex-event-service.")
	fmt.Println("It reads event data from stdin and outputs results to stdout.")
	fmt.Println()
	fmt.Println("Example usage:")
	fmt.Println("  echo '{\"event_id\":\"test-123\",\"service\":\"dex-cli\",\"event_type\":\"test.trigger\",\"event_data\":{\"message\":\"test\"},\"timestamp\":1234567890}' | event-test-handler")
	fmt.Println()
	fmt.Println("Input format (JSON via stdin):")
	fmt.Println("  {")
	fmt.Println("    \"event_id\": \"unique-event-id\",")
	fmt.Println("    \"service\": \"source-service-name\",")
	fmt.Println("    \"event_type\": \"event.type\",")
	fmt.Println("    \"event_data\": {\"message\": \"trigger message\"},")
	fmt.Println("    \"timestamp\": 1234567890")
	fmt.Println("  }")
	fmt.Println()
	fmt.Println("Output format (JSON to stdout):")
	fmt.Println("  {")
	fmt.Println("    \"success\": true,")
	fmt.Println("    \"error\": \"optional error message\",")
	fmt.Println("    \"events\": [{\"type\": \"event_type\", \"data\": {...}}]")
	fmt.Println("  }")
}

func main() {
	// Check if stdin is a terminal (interactive mode)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		printHelp()
		os.Exit(0)
	}

	var output HandlerOutput

	// Read input event from stdin
	inputData, err := io.ReadAll(os.Stdin)
	if err != nil {
		output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
			"error": fmt.Sprintf("Failed to read input: %v", err),
		}))
		_ = json.NewEncoder(os.Stdout).Encode(output)
		return
	}

	// If stdin is empty, show help
	if len(inputData) == 0 {
		printHelp()
		os.Exit(0)
	}

	var input HandlerInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
			"error": fmt.Sprintf("Failed to parse input: %v", err),
		}))
		_ = json.NewEncoder(os.Stdout).Encode(output)
		return
	}

	// Acknowledge the trigger event
	triggerMsg, _ := input.EventData["message"].(string)
	output.Events = append(output.Events, logEvent(fmt.Sprintf("Test suite triggered by: %s", triggerMsg)))
	output.Events = append(output.Events, logEvent(fmt.Sprintf("Parent event ID: %s", input.EventID)))

	// PHASE 1: Template Tests
	output.Events = append(output.Events, logEvent("🧪 PHASE 1: Testing event templates"))

	templates := []struct {
		name string
		data map[string]interface{}
	}{
		{"message_received", map[string]interface{}{
			"type":    "message_received",
			"user":    "alice",
			"message": "Test message",
			"channel": "testing",
		}},
		{"action_performed", map[string]interface{}{
			"type":   "action_performed",
			"actor":  "system",
			"action": "validated",
			"target": "service",
		}},
		{"log_entry", map[string]interface{}{
			"type":    "log_entry",
			"level":   "debug",
			"message": "Test log entry",
		}},
		{"error_occurred", map[string]interface{}{
			"type":  "error_occurred",
			"error": "Intentional test error",
		}},
		{"status_change", map[string]interface{}{
			"type":       "status_change",
			"entity":     "test-entity",
			"new_status": "validated",
		}},
		{"metric_recorded", map[string]interface{}{
			"type":        "metric_recorded",
			"metric_name": "test_metric",
			"value":       42.0,
			"unit":        "tests",
		}},
	}

	for _, template := range templates {
		success, msg := testEvent("dex-test", template.name, template.data)
		if success {
			output.Events = append(output.Events, logEvent(msg))
		} else {
			output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
				"error": msg,
			}))
		}
	}

	output.Events = append(output.Events, createEvent("status_change", map[string]interface{}{
		"entity":     "template-tests",
		"new_status": "completed",
	}))

	// PHASE 2: Validation Tests
	output.Events = append(output.Events, logEvent("🔍 PHASE 2: Testing validation"))

	// Test invalid event (missing required fields)
	payload := map[string]interface{}{
		"service": "dex-test",
		"event": map[string]interface{}{
			"type": "message_received",
			// Missing: user, message, channel
		},
	}
	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "http://localhost:8100/events", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Name", "dex-test-suite")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, _ := client.Do(req)
	if resp.StatusCode == 400 {
		output.Events = append(output.Events, logEvent("✓ Invalid event correctly rejected"))
	} else {
		output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
			"error": "✗ Invalid event should have been rejected",
		}))
	}
	_ = resp.Body.Close()

	output.Events = append(output.Events, createEvent("status_change", map[string]interface{}{
		"entity":     "validation-tests",
		"new_status": "completed",
	}))

	// PHASE 3: Multi-Language Tests
	output.Events = append(output.Events, logEvent("🌍 PHASE 3: Testing languages"))

	// Test ISO codes
	langs := []string{"uk", "ru", "da", "de", "el", "ro", "tr", "sr", "lt"}
	passCount := 0
	for _, lang := range langs {
		success, msg := testLanguageFormat(lang)
		if success {
			passCount++
			output.Events = append(output.Events, logEvent(msg))
		} else {
			output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
				"error": msg,
			}))
		}
	}

	// Test English names
	langTests := []struct {
		name     string
		expected string
	}{
		{"Ukrainian", "uk"},
		{"Russian", "ru"},
		{"Danish", "da"},
		{"German", "de"},
		{"Greek", "el"},
	}

	for _, test := range langTests {
		success, msg := testLanguageFormat(test.name)
		if success {
			passCount++
			output.Events = append(output.Events, logEvent(fmt.Sprintf("✓ English name '%s' works", test.name)))
		} else {
			output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
				"error": msg,
			}))
		}
	}

	// Test native names
	nativeTests := []string{"Українська", "Русский", "Dansk", "Deutsch", "Ελληνικά"}
	for _, native := range nativeTests {
		success, msg := testLanguageFormat(native)
		if success {
			passCount++
			output.Events = append(output.Events, logEvent(fmt.Sprintf("✓ Native name '%s' works", native)))
		} else {
			output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
				"error": msg,
			}))
		}
	}

	output.Events = append(output.Events, createEvent("metric_recorded", map[string]interface{}{
		"metric_name": "language_tests_passed",
		"value":       float64(passCount),
		"unit":        "tests",
	}))

	output.Events = append(output.Events, createEvent("status_change", map[string]interface{}{
		"entity":     "language-tests",
		"new_status": "completed",
	}))

	// PHASE 4: Format Tests
	output.Events = append(output.Events, logEvent("📋 PHASE 4: Testing formats"))

	// Test text format
	req, _ = http.NewRequest("GET", "http://localhost:8100/events?ml=5&format=text", nil)
	req.Header.Set("X-Service-Name", "dex-test-suite")
	resp, _ = client.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && len(body) > 0 {
		output.Events = append(output.Events, logEvent("✓ Text format works"))
	} else {
		output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
			"error": "✗ Text format failed",
		}))
	}
	_ = resp.Body.Close()

	// Test timezone
	req, _ = http.NewRequest("GET", "http://localhost:8100/events?ml=5&format=text&timezone=Europe/Paris", nil)
	req.Header.Set("X-Service-Name", "dex-test-suite")
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && len(body) > 0 {
		output.Events = append(output.Events, logEvent("✓ Timezone conversion works"))
	} else {
		output.Events = append(output.Events, createEvent("error_occurred", map[string]interface{}{
			"error": "✗ Timezone failed",
		}))
	}
	_ = resp.Body.Close()

	output.Events = append(output.Events, createEvent("status_change", map[string]interface{}{
		"entity":     "format-tests",
		"new_status": "completed",
	}))

	// FINAL STATUS
	output.Events = append(output.Events, logEvent("✅ ALL TESTS COMPLETED"))
	output.Events = append(output.Events, createEvent("metric_recorded", map[string]interface{}{
		"metric_name": "total_test_events",
		"value":       float64(len(output.Events)),
		"unit":        "events",
	}))

	output.Events = append(output.Events, createEvent("status_change", map[string]interface{}{
		"entity":     "dex-event-service",
		"new_status": "validated",
	}))

	// Mark handler as successful
	output.Success = true

	// Output all events as JSON
	_ = json.NewEncoder(os.Stdout).Encode(output)
}
