package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/EasterCompany/dex-event-service/types"
)

var (
	registry      *types.HandlerRegistry
	dexterBinPath string
)

// Initialize loads the handler registry from config
func Initialize() error {
	// Get home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %v", err)
	}

	// Set Dexter bin path
	dexterBinPath = filepath.Join(homeDir, "Dexter", "bin")

	// Load handler config
	configPath := filepath.Join(homeDir, "Dexter", "config", "event-handlers.json")

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Create default empty config
		registry = &types.HandlerRegistry{
			Handlers: make(map[string]types.HandlerConfig),
		}

		// Create directory if needed
		configDir := filepath.Dir(configPath)
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %v", err)
		}

		// Populate with defaults immediately
		fmt.Println("DEBUG: Populating defaults for new file...")
		if ensureDefaultHandlers() {
			fmt.Printf("DEBUG: ensureDefaultHandlers returned true. Registry has %d handlers.\n", len(registry.Handlers))
		} else {
			fmt.Printf("DEBUG: ensureDefaultHandlers returned false. Registry has %d handlers.\n", len(registry.Handlers))
		}

		// Write config
		if err := saveRegistry(configPath); err != nil {
			return err
		}

		fmt.Println("Created handler registry with defaults at", configPath)
		return nil
	}

	// Load existing config
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read handler config: %v", err)
	}

	registry = &types.HandlerRegistry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return fmt.Errorf("failed to parse handler config: %v", err)
	}

	fmt.Printf("DEBUG: Loaded existing config with %d handlers.\n", len(registry.Handlers))

	// Ensure defaults exist (migrations)
	if ensureDefaultHandlers() {
		if err := saveRegistry(configPath); err != nil {
			fmt.Printf("Warning: Failed to save updated handler registry: %v\n", err)
		} else {
			fmt.Println("Updated handler registry with new defaults")
		}
	}

	fmt.Printf("Loaded %d handlers from config\n", len(registry.Handlers))
	return nil
}

// ensureDefaultHandlers adds or updates default handlers in the registry. Returns true if changes were made.
func ensureDefaultHandlers() bool {
	if registry.Handlers == nil {
		registry.Handlers = make(map[string]types.HandlerConfig)
	}

	updated := false

	// Define default handlers
	defaults := map[string]types.HandlerConfig{
		"test": {
			Name:        "test",
			Binary:      "event-test-handler",
			Description: "Validates the service",
			Timeout:     10,
			EventTypes:  []string{},
			OutputEvent: "multiple", // Explicitly set this if needed
		},
		"transcription": {
			Name:        "transcription",
			Binary:      "event-transcription-handler",
			Description: "Analyzes transcriptions for engagement",
			Timeout:     300,
			DebounceKey: "channel_id",
			EventTypes:  []string{"transcription", "message.transcribed", "user_transcribed", "messaging.user.transcribed"},
		},
		"public-message-handler": {
			Name:        "public-message-handler",
			Binary:      "event-public-message-handler",
			Description: "Handles public channel messages for engagement",
			Timeout:     60,
			DebounceKey: "channel_id",
			EventTypes:  []string{"messaging.user.sent_message"},
			Filters:     map[string]string{"server_id": "!empty"},
		},
		"private-message-handler": {
			Name:        "private-message-handler",
			Binary:      "event-private-message-handler",
			Description: "Handles private messages (DMs) for engagement",
			Timeout:     60,
			DebounceKey: "user_id",
			EventTypes:  []string{"messaging.user.sent_message"},
			Filters:     map[string]string{"server_id": "empty"},
		},
	}

	fmt.Printf("DEBUG: checking %d defaults against %d existing.\n", len(defaults), len(registry.Handlers))

	for name, config := range defaults {
		existing, exists := registry.Handlers[name]
		if !exists {
			fmt.Printf("Handler '%s' missing, adding default configuration\n", name)
			registry.Handlers[name] = config
			updated = true
			continue
		}

		// Check if key fields match (simple update check)
		// For now, we just force update the critical fields to match code definition
		// This ensures that if we change the binary name or event types in code, it propagates to config

		// Force update of defaults to ensure they match code expectations
		// This effectively makes the code the source of truth for these handlers
		if existing.Binary != config.Binary ||
			existing.Description != config.Description ||
			existing.Timeout != config.Timeout ||
			existing.DebounceKey != config.DebounceKey ||
			!equalStringSlices(existing.EventTypes, config.EventTypes) ||
			!equalMapStrings(existing.Filters, config.Filters) {

			fmt.Printf("Handler '%s' configuration outdated, updating to default\n", name)
			// Merge/Overwrite
			// Preserve things the user might have changed?
			// For "managed" handlers, we probably want to enforce our config.
			registry.Handlers[name] = config
			updated = true
		} else {
			// Check event types specifically (already checked above for equality of structs)
			// Now only check if some custom field was added
			if !equalStringSlices(existing.EventTypes, config.EventTypes) {
				fmt.Printf("Handler '%s' event types outdated, updating\n", name)
				registry.Handlers[name] = config
				updated = true
			}
		}
	}

	return updated
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] { // strict order check
			return false
		}
	}
	return true
}

func equalMapStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// saveRegistry writes the registry back to disk
func saveRegistry(path string) error {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %v", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write registry: %v", err)
	}

	return nil
}

// GetHandler returns a handler by name
func GetHandler(name string) (*types.HandlerConfig, bool) {
	if registry == nil {
		return nil, false
	}

	handler, exists := registry.Handlers[name]
	if !exists {
		return nil, false
	}

	return &handler, true
}

// GetHandlersForEventType returns all handlers configured for a specific event type
func GetHandlersForEventType(eventType string) []types.HandlerConfig {
	if registry == nil {
		return nil
	}

	var handlers []types.HandlerConfig
	for _, handler := range registry.Handlers {
		// Check if this handler applies to this event type
		for _, et := range handler.EventTypes {
			if et == eventType {
				handlers = append(handlers, handler)
				break
			}
		}
	}

	return handlers
}

// GetBinaryPath returns the full path to a handler binary
func GetBinaryPath(binaryName string) string {
	return filepath.Join(dexterBinPath, binaryName)
}
