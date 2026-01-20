package handlers

import (
	"github.com/EasterCompany/dex-event-service/types"
)

// Static registry of handler configurations
// This replaces the old JSON-based config.go
var handlerConfigs = map[string]types.HandlerConfig{
	"test": {
		Name:        "test",
		Description: "Validates the service",
		Timeout:     10,
		EventTypes:  []string{}, // Triggered manually
	},
	"transcription-handler": {
		Name:        "transcription-handler",
		Description: "Analyzes transcriptions for engagement",
		Timeout:     3600,
		DebounceKey: "channel_id",
		EventTypes:  []string{"transcription", "message.transcribed", "user_transcribed", "messaging.user.transcribed"},
	},
	"public-message-handler": {
		Name:        "public-message-handler",
		Description: "Handles public channel messages for engagement",
		Timeout:     3600,
		DebounceKey: "channel_id",
		EventTypes:  []string{"messaging.user.sent_message"},
		Filters:     map[string]string{"server_id": "!empty"},
	},
	"greeting-handler": {
		Name:        "greeting-handler",
		Description: "Greets users when bot joins a voice channel",
		Timeout:     900,
		DebounceKey: "channel_id",
		EventTypes:  []string{"messaging.bot.joined_voice"},
	},
	"webhook-handler": {
		Name:        "webhook-handler",
		Description: "Handles webhook messages",
		Timeout:     900,
		EventTypes:  []string{"messaging.webhook.message"},
	},
	"private-message-handler": {
		Name:        "private-message-handler",
		Description: "Handles private messages (DMs)",
		Timeout:     3600,
		DebounceKey: "channel_id",
		EventTypes:  []string{"messaging.user.sent_message"},
		Filters:     map[string]string{"server_id": "empty"}, // DMs usually have empty server_id
	},
	"guardian-handler": {
		Name:               "guardian-handler",
		IsBackgroundWorker: true,
		Timeout:            0, // Managed by its own internal loop
	},
	"imaginator-handler": {
		Name:               "imaginator-handler",
		IsBackgroundWorker: true,
		Timeout:            0,
	},
	"profiler-handler": {
		Name:        "profiler-handler",
		Description: "Accumulates analytical signals into user profiles",
		Timeout:     30,
		EventTypes: []string{
			"analysis.user.message_signals",
			"messaging.bot.sent_message",
			"messaging.user.sent_message",
			"messaging.user.transcribed",
			"messaging.webhook.message",
		},
	},
	"analyzer-handler": {
		Name:               "analyzer-handler",
		IsBackgroundWorker: true,
		Timeout:            0,
	},
	"courier-handler": {
		Name:               "courier-handler",
		IsBackgroundWorker: true,
		Timeout:            0,
	},
}

// Initialize performs any startup tasks for handlers (now mostly a no-op or just logging)
func Initialize() error {
	// We could validate the internalRegistry here against handlerConfigs
	return nil
}

// GetHandler returns a handler config by name
func GetHandler(name string) (*types.HandlerConfig, bool) {
	config, exists := handlerConfigs[name]
	if !exists {
		return nil, false
	}
	return &config, true
}

// GetHandlersForEventType returns all handlers configured for a specific event type
func GetHandlersForEventType(eventType string) []types.HandlerConfig {
	var handlers []types.HandlerConfig
	for _, config := range handlerConfigs {
		for _, et := range config.EventTypes {
			if et == eventType {
				handlers = append(handlers, config)
				break
			}
		}
	}
	return handlers
}
