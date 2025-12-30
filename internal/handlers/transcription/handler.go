package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/types"
)

func emitEvent(serviceURL string, eventData map[string]interface{}) error {
	reqBody := map[string]interface{}{
		"service": "dex-discord-service",
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

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	transcription, _ := input.EventData["transcription"].(string)
	channelID, _ := input.EventData["channel_id"].(string)
	userID, _ := input.EventData["user_id"].(string)
	userName, _ := input.EventData["user_name"].(string)
	channelName, _ := input.EventData["channel_name"].(string)
	serverID, _ := input.EventData["server_id"].(string)
	serverName, _ := input.EventData["server_name"].(string)

	log.Printf("transcription-handler processing for user %s: %s", userName, transcription)

	deps.Discord.UpdateBotStatus("Thinking...", "online", 3)
	defer deps.Discord.UpdateBotStatus("Listening for events...", "online", 2)

	// Fetch context
	contextHistory, err := deps.Discord.FetchContext(channelID, 25)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
	}

	// 1. Check Engagement
	prompt := fmt.Sprintf("Analyze if Dexter should respond to this voice transcription. Output <ENGAGE/> or <IGNORE/>.\n\nContext:\n%s\n\nCurrent Transcription:\n%s", contextHistory, transcription)
	engagementRaw, err := deps.Ollama.Generate("dex-fast-engagement-model", prompt, nil)
	if err != nil {
		log.Printf("Engagement check failed: %v", err)
		return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
	}

	shouldEngage := strings.Contains(engagementRaw, "<ENGAGE/>")

	engagementReason := "Evaluated by dex-fast-engagement-model"

	userCount, err := deps.Discord.GetVoiceChannelUserCount(channelID)
	if err != nil {
		log.Printf("Failed to get voice channel user count: %v", err)
	} else {
		log.Printf("Voice channel user count: %d", userCount)
		if userCount <= 2 {
			if !shouldEngage {
				log.Printf("Forcing engagement because user is alone with Dexter (count: %d)", userCount)
				shouldEngage = true
				engagementReason = fmt.Sprintf("Forced engagement: User count is %d (Alone with Dexter)", userCount)
			}
		}
	}

	log.Printf("Engagement decision for transcription: %s (%v)", engagementRaw, shouldEngage)

	decisionStr := "IGNORE"
	if shouldEngage {
		decisionStr = "ENGAGE"
	}

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         decisionStr,
		"reason":           engagementReason,
		"handler":          "transcription-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  transcription,
		"timestamp":        time.Now().Unix(),
		"engagement_model": "dex-fast-engagement-model",
		"context_history":  contextHistory,
		"engagement_raw":   engagementRaw,
		"user_count":       userCount,
	}

	if err := emitEvent(deps.EventServiceURL, engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	// 2. Engage if needed
	if shouldEngage {
		deps.Discord.UpdateBotStatus("Thinking of response...", "online", 0)

		prompt := fmt.Sprintf("Context:\n%s\n\nUser (%s) Said: %s", contextHistory, userName, transcription)
		responseModel := "dex-fast-transcription-model"
		response, err := deps.Ollama.Generate(responseModel, prompt, nil)

		if err != nil {
			log.Printf("Response generation failed: %v", err)
		} else {
			log.Printf("Generated response: %s", response)

			botResponseEventData := map[string]interface{}{
				"type":           "messaging.bot.voice_response",
				"source":         "discord",
				"user_id":        "dexter-bot",
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

			if err := emitEvent(deps.EventServiceURL, botResponseEventData); err != nil {
				log.Printf("Failed to emit bot response event: %v", err)
			}

			deps.Discord.UpdateBotStatus("Generating speech...", "online", 0)

			ttsPayload := map[string]string{"text": response, "language": "en"}
			ttsJson, _ := json.Marshal(ttsPayload)

			ttsResp, ttsErr := http.Post(deps.TTSServiceURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
			if ttsErr != nil {
				log.Printf("TTS generation failed: %v", ttsErr)
			} else {
				defer func() { _ = ttsResp.Body.Close() }()
				if ttsResp.StatusCode == 200 {
					audioBytes, _ := io.ReadAll(ttsResp.Body)

					deps.Discord.UpdateBotStatus("Speaking...", "online", 0)
					if playErr := deps.Discord.PlayAudio(audioBytes); playErr != nil {
						log.Printf("Failed to play audio in Discord: %v", playErr)
					}
				} else {
					log.Printf("TTS service error: %d", ttsResp.StatusCode)
				}
			}
		}
	}

	return types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}, nil
}
