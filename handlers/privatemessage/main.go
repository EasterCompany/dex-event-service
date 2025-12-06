package main

import (
	"bytes"
	"encoding/base64"
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
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"`
	Stream bool     `json:"stream"`
}

type OllamaGenerateResponse struct {
	Response string `json:"response"`
}

func generateOllamaResponse(model, prompt string, images []string) (string, error) {
	reqBody := OllamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Images: images,
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

func downloadImageAsBase64(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
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
	// Use channel_id to filter DM context as well (since we set it in event)
	url := fmt.Sprintf("%s/events?channel=%s&max_length=50&order=desc&format=text&exclude_types=engagement.decision", getEventServiceURL(), channelID)

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

func updateBotStatus(text string, status string, activityType int) {
	serviceURL := getDiscordServiceURL()
	reqBody := map[string]interface{}{
		"status_text":   text,
		"online_status": status,
		"activity_type": activityType,
	}
	jsonData, _ := json.Marshal(reqBody)

	// Synchronous call to ensure it completes before process exit
	resp, err := http.Post(serviceURL+"/status", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
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

	// Process attachments
	var attachments []map[string]interface{}
	if att, ok := input.EventData["attachments"].([]interface{}); ok {
		for _, a := range att {
			if m, ok := a.(map[string]interface{}); ok {
				attachments = append(attachments, m)
			}
		}
	}

	log.Printf("private-message-handler processing for user %s: %s (attachments: %d)", userID, content, len(attachments))

	// Set status: Thinking
	updateBotStatus("Thinking...", "online", 3)                   // 3 = Watching
	defer updateBotStatus("Listening for events...", "online", 2) // 2 = Listening

	// --- Visual Analysis Phase ---
	visualContext := ""
	if len(attachments) > 0 {
		for _, att := range attachments {
			contentType, _ := att["content_type"].(string)
			url, _ := att["url"].(string)
			filename, _ := att["filename"].(string)

			// Check if image
			if strings.HasPrefix(contentType, "image/") {
				updateBotStatus("Analyzing image...", "online", 3)
				log.Printf("Downloading image: %s", filename)
				base64Img, err := downloadImageAsBase64(url)
				if err != nil {
					log.Printf("Failed to download image %s: %v", filename, err)
					continue
				}

				log.Printf("Generating visual description for %s...", filename)
				description, err := generateOllamaResponse("dex-vision-model", "Describe this image concisely.", []string{base64Img})
				if err != nil {
					log.Printf("Vision model failed for %s: %v", filename, err)
					continue
				}

				log.Printf("Visual description for %s: %s", filename, description)
				visualContext += fmt.Sprintf("\n[Attachment: %s (Image) - Description: %s]", filename, description)

				// Emit child event for analysis
				analysisEvent := map[string]interface{}{
					"type":            "analysis.visual.completed",
					"parent_event_id": input.EventID,
					"handler":         "private-message-handler",
					"filename":        filename,
					"description":     description,
					"timestamp":       time.Now().Unix(),
				}
				_ = emitEvent(analysisEvent)
			}
		}
	}

	// Append visual context to content
	if visualContext != "" {
		content += visualContext
	}

	// Fetch context
	contextHistory, err := fetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
	}

	// 1. Check Engagement
	prompt := fmt.Sprintf("Context:\n%s\n\nCurrent Message:\n%s", contextHistory, content)
	engagementRaw, err := generateOllamaResponse("dex-engagement-model", prompt, nil)
	if err != nil {
		log.Printf("Engagement check failed: %v", err)
		return // Or return error event
	}

	engagementDecision := strings.ToUpper(strings.TrimSpace(engagementRaw))
	shouldEngage := strings.Contains(engagementDecision, "TRUE")

	log.Printf("Engagement decision: %s (%v)", engagementDecision, shouldEngage)

	// Construct and emit engagement decision event immediately
	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         engagementDecision,
		"reason":           "Evaluated by dex-engagement-model",
		"handler":          "private-message-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  content,
		"timestamp":        time.Now().Unix(),
		"engagement_model": "dex-engagement-model",
		"context_history":  contextHistory,
		"engagement_raw":   engagementRaw,
	}

	if err := emitEvent(engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	// 2. Engage if needed
	var response string
	if shouldEngage {
		updateBotStatus("Typing response...", "online", 0) // 0 = Playing (Game)

		prompt := fmt.Sprintf("Context:\n%s\n\nUser: %s", contextHistory, content)
		var err error
		responseModel := "dex-private-message-model"
		response, err = generateOllamaResponse(responseModel, prompt, nil)

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
	// We don't need to return events here anymore as we emitted them directly.
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
