package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/EasterCompany/dex-event-service/types"
)

func TestGetHandler(t *testing.T) {
	// Setup mock registry
	registry = &types.HandlerRegistry{
		Handlers: map[string]types.HandlerConfig{
			"test_handler": {
				Name:        "test_handler",
				Binary:      "test_binary",
				Description: "Test handler",
				Timeout:     30,
			},
		},
	}

	// Test getting existing handler
	handler, exists := GetHandler("test_handler")
	if !exists {
		t.Fatal("Expected handler to exist")
	}

	if handler.Name != "test_handler" {
		t.Errorf("Expected name 'test_handler', got '%s'", handler.Name)
	}

	// Test getting non-existent handler
	_, exists = GetHandler("nonexistent")
	if exists {
		t.Error("Expected handler to not exist")
	}
}

func TestGetHandlersForEventType(t *testing.T) {
	// Setup mock registry
	registry = &types.HandlerRegistry{
		Handlers: map[string]types.HandlerConfig{
			"audio_handler": {
				Name:       "audio_handler",
				EventTypes: []string{"audio_received"},
			},
			"message_handler": {
				Name:       "message_handler",
				EventTypes: []string{"message_received", "message_edited"},
			},
			"generic_handler": {
				Name:       "generic_handler",
				EventTypes: []string{},
			},
		},
	}

	// Test getting handlers for audio_received
	handlers := GetHandlersForEventType("audio_received")
	if len(handlers) != 1 {
		t.Errorf("Expected 1 handler for audio_received, got %d", len(handlers))
	}
	if len(handlers) > 0 && handlers[0].Name != "audio_handler" {
		t.Errorf("Expected 'audio_handler', got '%s'", handlers[0].Name)
	}

	// Test getting handlers for message_received
	handlers = GetHandlersForEventType("message_received")
	if len(handlers) != 1 {
		t.Errorf("Expected 1 handler for message_received, got %d", len(handlers))
	}

	// Test getting handlers for non-existent event type
	handlers = GetHandlersForEventType("nonexistent_event")
	if len(handlers) != 0 {
		t.Errorf("Expected 0 handlers for nonexistent_event, got %d", len(handlers))
	}
}

func TestGetBinaryPath(t *testing.T) {
	homeDir, _ := os.UserHomeDir()
	expected := filepath.Join(homeDir, "Dexter", "bin", "test_binary")

	// Initialize to set dexterBinPath
	dexterBinPath = filepath.Join(homeDir, "Dexter", "bin")

	result := GetBinaryPath("test_binary")
	if result != expected {
		t.Errorf("Expected path '%s', got '%s'", expected, result)
	}
}

func TestInitialize_CreatesEmptyConfigIfMissing(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()

	// Set HOME to temp directory for this test
	oldHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", oldHome) }()

	// Initialize should create empty config
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify config file was created
	configPath := filepath.Join(tmpDir, "Dexter", "config", "event-handlers.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Expected config file to be created")
	}

	// Verify registry is initialized
	if registry == nil {
		t.Fatal("Expected registry to be initialized")
	}

	if registry.Handlers == nil {
		t.Error("Expected registry.Handlers to be initialized")
	}
}
