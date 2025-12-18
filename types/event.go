package types

import (
	"encoding/json"
	"time"
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

// EventType is a custom type for our event types
type EventType string

// Constants for our event types
const (
	// Messaging Events
	EventTypeMessagingUserJoinedVoice     EventType = "messaging.user.joined_voice"
	EventTypeMessagingUserLeftVoice       EventType = "messaging.user.left_voice"
	EventTypeMessagingUserSentMessage     EventType = "messaging.user.sent_message"
	EventTypeMessagingBotSentMessage      EventType = "messaging.bot.sent_message"
	EventTypeMessagingBotStatusUpdate     EventType = "messaging.bot.status_update"
	EventTypeMessagingUserSpeakingStarted EventType = "messaging.user.speaking.started"
	EventTypeMessagingUserSpeakingStopped EventType = "messaging.user.speaking.stopped"
	EventTypeMessagingUserTranscribed     EventType = "messaging.user.transcribed"
	EventTypeMessagingUserJoinedServer    EventType = "messaging.user.joined_server"
	EventTypeMessagingBotVoiceResponse    EventType = "messaging.bot.voice_response"
	EventTypeMessagingWebhookMessage      EventType = "messaging.webhook.message"

	// Moderation Events
	EventTypeModerationExplicitContentDeleted EventType = "moderation.explicit_content.deleted"

	// Analysis Events
	EventTypeAnalysisVisualCompleted EventType = "analysis.visual.completed"
	EventTypeAnalysisLinkCompleted   EventType = "analysis.link.completed"

	// System Events
	EventTypeSystemStatusChange EventType = "system.status.change"

	// CLI Events
	EventTypeCLICommand EventType = "system.cli.command"
	EventTypeCLIStatus  EventType = "system.cli.status"

	// System Notifications
	EventTypeSystemNotificationGenerated EventType = "system.notification.generated"
)

// GenericMessagingEvent contains common fields for all messaging-related events
type GenericMessagingEvent struct {
	Type        EventType `json:"type"`
	Source      string    `json:"source"` // e.g., "discord", "slack"
	UserID      string    `json:"user_id"`
	UserName    string    `json:"user_name"`
	ChannelID   string    `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	ServerID    string    `json:"server_id"`
	ServerName  string    `json:"server_name,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// UserSentMessageEvent is the payload for EventTypeMessagingUserSentMessage
type UserSentMessageEvent struct {
	GenericMessagingEvent
	MessageID string `json:"message_id"`
	Content   string `json:"content"`
}

// UserVoiceStateChangeEvent is the payload for voice channel join/leave events
type UserVoiceStateChangeEvent struct {
	GenericMessagingEvent
}

// UserServerEvent is the payload for server-level user events
type UserServerEvent struct {
	GenericMessagingEvent
}

// BotStatusUpdateEvent is the payload for the bot's own status changes
type BotStatusUpdateEvent struct {
	Type      EventType `json:"type"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	Details   string    `json:"details"`
	Timestamp time.Time `json:"timestamp"`
}

// UserSpeakingEvent is for when a user starts or stops speaking
type UserSpeakingEvent struct {
	GenericMessagingEvent
	SSRC uint32 `json:"ssrc"`
}

// UserTranscribedEvent is for when a user's speech is transcribed
type UserTranscribedEvent struct {
	GenericMessagingEvent
	Transcription string `json:"transcription"`
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
