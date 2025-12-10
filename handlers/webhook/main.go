package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/EasterCompany/dex-event-service/types"
)

// ServiceMap minimal structure for reading port
type ServiceMap struct {
	Services map[string][]struct {
		ID   string `json:"id"`
		Port string `json:"port"`
	} `json:"services"`
}

func getEventServiceURL() string {
	// Try to read service-map.json
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			// Check all categories
			for _, cat := range sm.Services {
				for _, service := range cat {
					if service.ID == "dex-event-service" {
						return fmt.Sprintf("http://localhost:%s", service.Port)
					}
				}
			}
		}
	}
	return "http://localhost:8082" // Fallback
}

func emitEvent(eventData map[string]interface{}) error {
	serviceURL := getEventServiceURL()
	reqBody := map[string]interface{}{
		"service": "dex-event-service",
		"event":   eventData,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := http.Post(serviceURL+"/events", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing event service response body: %v", err)
		}
	}()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("event service error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func main() {
	// Read HandlerInput from stdin
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("Error reading stdin: %v", err)
	}

	var input types.HandlerInput
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		log.Fatalf("Error unmarshaling HandlerInput: %v", err)
	}

	log.Printf("Webhook handler received event: %s", input.EventID)

	// Emit a 'processed' event to confirm handling (and to test the capability)
	// In the future, this is where we'd parse the webhook content and maybe post a summary
	processedEvent := map[string]interface{}{
		"type":            "webhook.processed",
		"parent_event_id": input.EventID,
		"handler":         "webhook-handler",
		"status":          "acknowledged",
	}

	if err := emitEvent(processedEvent); err != nil {
		log.Printf("Failed to emit processed event: %v", err)
	}

	// Construct HandlerOutput
	output := types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}

	// Marshal HandlerOutput to JSON and print to stdout
	outputBytes, err := json.Marshal(output)
	if err != nil {
		log.Fatalf("Error marshaling HandlerOutput: %v", err)
	}

	fmt.Println(string(outputBytes))
}
