package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/EasterCompany/dex-event-service/types"
)

// ServiceMap minimal structure for reading port
type ServiceMap struct {
	Services map[string][]struct {
		ID   string `json:"id"`
		Port string `json:"port"`
	} `json:"services"`
}

func getDiscordServiceURL() string {
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			for _, service := range sm.Services["th"] {
				if service.ID == "dex-discord-service" {
					return fmt.Sprintf("http://127.0.0.1:%s", service.Port)
				}
			}
		}
	}
	return "http://127.0.0.1:8300" // Fallback
}

func getTTSServiceURL() string {
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			for _, service := range sm.Services["be"] {
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

	req, err := http.NewRequest("POST", serviceURL+"/audio/play", bytes.NewBuffer(audioData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Service-Name", "dex-event-service")

	client := &http.Client{Timeout: 30 * time.Second}
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

func main() {
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("Error reading stdin: %v", err)
	}

	var input types.HandlerInput
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		log.Fatalf("Error unmarshaling HandlerInput: %v", err)
	}

	// We only care about messaging.bot.joined_voice
	// The input includes EventData
	channelID, _ := input.EventData["channel_id"].(string)
	channelName, _ := input.EventData["channel_name"].(string)

	log.Printf("greeting-handler: Bot joined channel %s (%s)", channelName, channelID)

	// Pick a random greeting
	greetings := []string{
		"Hey",
		"Hello",
		"Yo",
		"Hi there",
		"Greetings",
		"What's up",
		"How's it going",
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	greeting := greetings[r.Intn(len(greetings))]

	log.Printf("Selected greeting: %s", greeting)

	updateBotStatus("Greeting...", "online", 0)

	ttsURL := getTTSServiceURL()
	ttsPayload := map[string]string{"text": greeting, "language": "en"}
	ttsJson, _ := json.Marshal(ttsPayload)

	ttsResp, ttsErr := http.Post(ttsURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
	if ttsErr != nil {
		log.Printf("TTS generation failed: %v", ttsErr)
	} else {
		defer func() { _ = ttsResp.Body.Close() }()
		if ttsResp.StatusCode == 200 {
			audioBytes, _ := io.ReadAll(ttsResp.Body)

			// Give discord voice connection a moment to settle?
			// The event is emitted after join, but maybe give it 500ms
			time.Sleep(500 * time.Millisecond)

			updateBotStatus("Speaking...", "online", 0)
			if playErr := playAudioInDiscord(audioBytes); playErr != nil {
				log.Printf("Failed to play audio in Discord: %v", playErr)
			}
		} else {
			log.Printf("TTS service error: %d", ttsResp.StatusCode)
		}
	}

	updateBotStatus("Listening for events...", "online", 2)

	output := types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}
	outputBytes, _ := json.Marshal(output)
	fmt.Println(string(outputBytes))
}
