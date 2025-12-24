package templates

import (
	"github.com/EasterCompany/dex-event-service/types"
	"testing"
)

func TestValidate_ValidEvent(t *testing.T) {
	eventData := map[string]interface{}{
		"type":    "message_received",
		"user":    "test_user",
		"message": "Hello world",
		"channel": "general",
	}

	errors := Validate("message_received", eventData)
	if len(errors) != 0 {
		t.Errorf("Expected no validation errors, got %d: %v", len(errors), errors)
	}
}

func TestValidate_MissingRequiredField(t *testing.T) {
	eventData := map[string]interface{}{
		"type": "message_received",
		"user": "test_user",
		// missing "message" and "channel"
	}

	errors := Validate("message_received", eventData)
	if len(errors) != 2 {
		t.Errorf("Expected 2 validation errors, got %d", len(errors))
	}
}

func TestValidate_InvalidEventType(t *testing.T) {
	eventData := map[string]interface{}{
		"type": "invalid_type",
	}

	errors := Validate("invalid_type", eventData)
	if len(errors) == 0 {
		t.Error("Expected validation error for invalid event type")
	}

	if errors[0].Field != "type" {
		t.Errorf("Expected error field 'type', got '%s'", errors[0].Field)
	}
}

func TestValidate_WrongFieldType(t *testing.T) {
	eventData := map[string]interface{}{
		"type":        "metric_recorded",
		"metric_name": "response_time",
		"value":       "not_a_number", // should be number
	}

	errors := Validate("metric_recorded", eventData)
	if len(errors) == 0 {
		t.Error("Expected validation error for wrong field type")
	}

	// Check that the error is about the 'value' field
	foundValueError := false
	for _, err := range errors {
		if err.Field == "value" {
			foundValueError = true
			break
		}
	}

	if !foundValueError {
		t.Error("Expected validation error for 'value' field")
	}
}

func TestValidate_ExtraFieldsAllowed(t *testing.T) {
	eventData := map[string]interface{}{
		"type":        "log_entry",
		"level":       "info",
		"message":     "Test message",
		"extra_field": "this should be allowed",
	}

	errors := Validate("log_entry", eventData)
	if len(errors) != 0 {
		t.Errorf("Expected no validation errors for extra fields, got %d: %v", len(errors), errors)
	}
}

func TestValidate_AllEventTypes(t *testing.T) {
	testCases := []struct {
		name      string
		eventType string
		eventData map[string]interface{}
		wantError bool
	}{
		{
			name:      "message_received - valid",
			eventType: "message_received",
			eventData: map[string]interface{}{
				"user":    "testuser",
				"message": "hello",
				"channel": "general",
			},
			wantError: false,
		},
		{
			name:      "action_performed - valid",
			eventType: "action_performed",
			eventData: map[string]interface{}{
				"actor":  "admin",
				"action": "created",
				"target": "channel",
			},
			wantError: false,
		},
		{
			name:      "log_entry - valid",
			eventType: "log_entry",
			eventData: map[string]interface{}{
				"level":   "info",
				"message": "Test log",
			},
			wantError: false,
		},
		{
			name:      "error_occurred - valid",
			eventType: "error_occurred",
			eventData: map[string]interface{}{
				"error": "Test error",
			},
			wantError: false,
		},
		{
			name:      "status_change - valid",
			eventType: "status_change",
			eventData: map[string]interface{}{
				"entity":     "bot",
				"new_status": "online",
			},
			wantError: false,
		},
		{
			name:      "metric_recorded - valid",
			eventType: "metric_recorded",
			eventData: map[string]interface{}{
				"metric_name": "cpu_usage",
				"value":       75.5,
			},
			wantError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errors := Validate(tc.eventType, tc.eventData)
			hasError := len(errors) > 0

			if hasError != tc.wantError {
				t.Errorf("Expected error=%v, got error=%v (errors: %v)", tc.wantError, hasError, errors)
			}
		})
	}
}

func TestGetTemplateList(t *testing.T) {
	// Need to import types to use the constants
	eventTypes := GetTemplateList()

	if len(eventTypes) != 32 {

		t.Errorf("Expected 32 event types, got %d", len(eventTypes))

	}

	expectedTypes := map[string]bool{

		"message_received": true,

		"action_performed": true,

		"log_entry": true,

		"error_occurred": true,

		"status_change": true,

		"metric_recorded": true,

		string(types.EventTypeMessagingUserJoinedVoice): true,

		string(types.EventTypeMessagingUserLeftVoice): true,

		string(types.EventTypeMessagingUserSentMessage): true,

		string(types.EventTypeMessagingBotSentMessage): true,

		"messaging.bot.joined_voice": true,

		string(types.EventTypeMessagingBotVoiceResponse): true,

		string(types.EventTypeMessagingBotStatusUpdate): true,

		string(types.EventTypeMessagingUserSpeakingStarted): true,

		string(types.EventTypeMessagingUserSpeakingStopped): true,

		string(types.EventTypeMessagingUserTranscribed): true,

		string(types.EventTypeMessagingUserJoinedServer): true,

		string(types.EventTypeMessagingWebhookMessage): true,

		"webhook.processed": true,

		string(types.EventTypeModerationExplicitContentDeleted): true,

		string(types.EventTypeAnalysisVisualCompleted): true,

		string(types.EventTypeAnalysisLinkCompleted): true,

		string(types.EventTypeCLICommand): true,

		string(types.EventTypeCLIStatus): true,

		string(types.EventTypeSystemNotificationGenerated): true,

		string(types.EventTypeSystemBlueprintGenerated): true,

		string(types.EventTypeSystemAnalysisAudit): true,

		"voice_speaking_started": true,

		"voice_speaking_stopped": true,

		"voice_transcribed": true,

		"engagement.decision": true,

		"bot_response": true,
	}

	for _, typeName := range eventTypes {
		if !expectedTypes[typeName] {
			t.Errorf("Unexpected event type: %s", typeName)
		}
	}
}
