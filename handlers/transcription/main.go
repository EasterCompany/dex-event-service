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
						return fmt.Sprintf("http://127.0.0.1:%s", service.Port)
					}
				}
			}
		}
	}
	return "http://127.0.0.1:8100" // Fallback
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
					return fmt.Sprintf("http://127.0.0.1:%s", service.Port)
				}
			}
		}
	}
	return "http://127.0.0.1:8300" // Fallback
}

func getTTSServiceURL() string {
	// Try to read service-map.json
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			for _, service := range sm.Services["be"] { // TTS is 'be'
				if service.ID == "dex-tts-service" {
					return fmt.Sprintf("http://127.0.0.1:%s", service.Port)
				}
			}
		}
	}
	return "http://127.0.0.1:8200" // Fallback
}

func playAudioInDiscord(audioData []byte) error {
	serviceURL := getDiscordServiceURL()

	// POST the raw audio data to /audio/play
	req, err := http.NewRequest("POST", serviceURL+"/audio/play", bytes.NewBuffer(audioData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Service-Name", "dex-event-service") // Auth

	client := &http.Client{Timeout: 30 * time.Second} // Give time for playback setup
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord service returned %d: %s", resp.StatusCode, resp.Status)
	}
	return nil
}

func updateBotStatus(text string, status string, activityType int) {
	serviceURL := getDiscordServiceURL()
	reqBody := map[string]interface{}{
		"status_text":   text,
		"online_status": status,
		"activity_type": activityType,
	}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := http.Post(serviceURL+"/status", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
}

func fetchContext(channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	url := fmt.Sprintf("%s/events?channel=%s&max_length=12&order=desc&format=text&exclude_types=engagement.decision", getEventServiceURL(), channelID)

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

	transcription, _ := input.EventData["transcription"].(string)
	channelID, _ := input.EventData["channel_id"].(string)
	userID, _ := input.EventData["user_id"].(string)
	userName, _ := input.EventData["user_name"].(string)
	channelName, _ := input.EventData["channel_name"].(string)
	serverID, _ := input.EventData["server_id"].(string)
	serverName, _ := input.EventData["server_name"].(string)

	log.Printf("transcription-handler processing for user %s: %s", userName, transcription)

	updateBotStatus("Thinking...", "online", 3)
	defer updateBotStatus("Listening for events...", "online", 2)

	// Fetch context
	contextHistory, err := fetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
	}

	// 1. Check Engagement
	prompt := fmt.Sprintf("Context:\n%s\n\nCurrent Transcription:\n%s", contextHistory, transcription)
	engagementRaw, err := generateOllamaResponse("dex-fast-engagement-model", prompt)
	if err != nil {
		log.Printf("Engagement check failed: %v", err)
		return // Fail gracefully
	}

	engagementDecision := strings.ToUpper(strings.TrimSpace(engagementRaw))
	shouldEngage := strings.Contains(engagementDecision, "TRUE")

	log.Printf("Engagement decision: %s (%v)", engagementDecision, shouldEngage)

	// Construct and emit engagement decision event immediately
	decisionStr := "FALSE"
	if shouldEngage {
		decisionStr = "TRUE"
	}

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         decisionStr,
		"reason":           "Evaluated by dex-engagement-model",
		"handler":          "transcription-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  transcription,
		"timestamp":        time.Now().Unix(),
		"engagement_model": "dex-fast-engagement-model",
		"context_history":  contextHistory,
		"engagement_raw":   engagementRaw,
	}

	if err := emitEvent(engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	// 2. Engage if needed
	if shouldEngage {
		updateBotStatus("Thinking of response...", "online", 0) // Changed from Speaking to Thinking as TTS isn't instant yet

		prompt := fmt.Sprintf("Context:\n%s\n\nUser (%s) Said: %s", contextHistory, userName, transcription)
		var err error
		responseModel := "dex-fast-transcription-model"
		response, err := generateOllamaResponse(responseModel, prompt)

		if err != nil {
			log.Printf("Response generation failed: %v", err)
		} else {
			log.Printf("Generated response: %s", response)

			// Generate Audio via TTS
			updateBotStatus("Generating speech...", "online", 0)

			ttsURL := getTTSServiceURL()
			ttsPayload := map[string]string{"text": response, "language": "en"}
			ttsJson, _ := json.Marshal(ttsPayload)

			ttsResp, ttsErr := http.Post(ttsURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
			if ttsErr != nil {
				log.Printf("TTS generation failed: %v", ttsErr)
			} else {
				defer func() { _ = ttsResp.Body.Close() }()
				if ttsResp.StatusCode == 200 {
					audioBytes, _ := io.ReadAll(ttsResp.Body)

					// Play in Discord
					updateBotStatus("Speaking...", "online", 0)
					if playErr := playAudioInDiscord(audioBytes); playErr != nil {
						log.Printf("Failed to play audio in Discord: %v", playErr)
					}
				} else {
					log.Printf("TTS service error: %d", ttsResp.StatusCode)
				}
			}

			// Emit messaging.bot.sent_message directly to the event service for logging
			botResponseEventData := map[string]interface{}{
				"type":           "messaging.bot.sent_message",
				"source":         "discord",    // Indicate it originated from Discord context
				"user_id":        "dexter-bot", // Placeholder ID - better would be dynamic from discord service
				"user_name":      "Dexter",
				"channel_id":     channelID,
				"channel_name":   channelName,
				"server_id":      serverID,
				"server_name":    serverName,
				"content":        response,
				"timestamp":      time.Now().Format(time.RFC3339),
				"response_model": responseModel,
				"response_raw":   response,
				"raw_input":      prompt,
			}

			if err := emitEvent(botResponseEventData); err != nil {
				log.Printf("Failed to emit bot response event: %v", err)
			}
		}
	}
	// Construct HandlerOutput (empty as we emitted manually)
	output := types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}

	outputBytes, err := json.Marshal(output)
	if err != nil {
		log.Fatalf("Error marshaling HandlerOutput: %v", err)
	}

	fmt.Println(string(outputBytes))
}
