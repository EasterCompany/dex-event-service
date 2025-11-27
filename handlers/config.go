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

		// Write empty config
		if err := saveRegistry(configPath); err != nil {
			return err
		}

		fmt.Println("Created empty handler registry at", configPath)
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

	fmt.Printf("Loaded %d handlers from config\n", len(registry.Handlers))
	return nil
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
