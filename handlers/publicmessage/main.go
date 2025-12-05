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
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/types"
)

const OllamaURL = "http://127.0.0.1:11434"

type OllamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type OllamaGenerateResponse struct {
	Response string `json:"response"`
}

func generateOllamaResponse(model, prompt string) (string, error) {
	reqBody := OllamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(OllamaURL+"/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing ollama response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var response OllamaGenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	return response.Response, nil
}

// ServiceMap minimal structure for reading port
type ServiceMap struct {
	Services map[string][]struct {
		ID   string `json:"id"`
		Port string `json:"port"`
	} `json:"services"`
}

func getDiscordServiceURL() string {
	// Try to read service-map.json
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			for _, service := range sm.Services["th"] { // Discord is usually 'th' (Third Party?) or 'cs'
				if service.ID == "dex-discord-service" {
					return fmt.Sprintf("http://localhost:%s", service.Port)
				}
			}
		}
	}
	return "http://localhost:8081" // Fallback
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

func postToDiscord(channelID, content string, metadata map[string]interface{}) error {
	serviceURL := getDiscordServiceURL()
	reqBody := map[string]interface{}{
		"channel_id": channelID,
		"content":    content,
		"metadata":   metadata,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := http.Post(serviceURL+"/post", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing discord response body: %v", err)
		}
	}()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord service error %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func fetchContext(channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	url := fmt.Sprintf("%s/events?channel=%s&max_length=10&order=asc&format=text", getEventServiceURL(), channelID)

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing event service response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch context: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
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

	content, _ := input.EventData["content"].(string)
	channelID, _ := input.EventData["channel_id"].(string)
	userID, _ := input.EventData["user_id"].(string)
	mentionedBot, _ := input.EventData["mentioned_bot"].(bool)

	log.Printf("public-message-handler processing for user %s in channel %s: %s (mentioned: %v)", userID, channelID, content, mentionedBot)

	shouldEngage := false
	engagementReason := "Evaluated by dex-engagement-model"
	var engagementRaw string

	// Fetch context
	contextHistory, err := fetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
	}

	if mentionedBot {
		log.Printf("Bot was mentioned, forcing engagement.")
		shouldEngage = true
		engagementReason = "Direct mention"
	} else {
		// 1. Check Engagement
		prompt := fmt.Sprintf("Context:\n%s\n\nCurrent Message:\n%s", contextHistory, content)
		var err error
		engagementRaw, err = generateOllamaResponse("dex-engagement-model", prompt)
		if err != nil {
			log.Printf("Engagement check failed: %v", err)
			// Fail gracefully, maybe don't engage if model fails
		} else {
			engagementDecision := strings.ToUpper(strings.TrimSpace(engagementRaw))
			shouldEngage = strings.Contains(engagementDecision, "TRUE")
			log.Printf("Engagement decision: %s (%v)", engagementDecision, shouldEngage)
		}
	}

	// Construct a child event for engagement decision
	decisionStr := "FALSE"
	if shouldEngage {
		decisionStr = "TRUE"
	}

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         decisionStr,
		"reason":           engagementReason,
		"handler":          "public-message-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  content,
		"timestamp":        time.Now().Unix(),
		"engagement_model": "dex-engagement-model",
		"context_history":  contextHistory,
		"engagement_raw":   engagementRaw,
	}

	// Emit decision immediately
	if err := emitEvent(engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	// 2. Engage if needed
	if shouldEngage {
		prompt := fmt.Sprintf("Context:\n%s\n\nUser: %s", contextHistory, content)
		var err error
		responseModel := "dex-public-message-model"
		response, err := generateOllamaResponse(responseModel, prompt)

		if err != nil {
			log.Printf("Response generation failed: %v", err)
		} else {
			log.Printf("Generated response: %s", response)

			metadata := map[string]interface{}{
				"response_model": responseModel,
				"response_raw":   response,
				"raw_input":      prompt,
			}

			if err := postToDiscord(channelID, response, metadata); err != nil {
				log.Printf("Failed to post to discord: %v", err)
			}
		}
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
