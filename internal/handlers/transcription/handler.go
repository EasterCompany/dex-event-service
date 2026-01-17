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
	"github.com/EasterCompany/dex-event-service/internal/handlers/profiler"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
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
	summaryModel := "dex-summary-model"
	contextHistory, err := smartcontext.Get(ctx, deps.Redis, deps.Ollama, channelID, summaryModel)
	if err != nil {
		log.Printf("Warning: Failed to fetch context from smartcontext: %v, falling back to legacy", err)
		contextHistory, _ = deps.Discord.FetchContext(channelID, 25)
	}

	// 1. Check Engagement
	prompt := fmt.Sprintf("Analyze if Dexter should respond to this message (from voice transcription). Output <ENGAGE/> or <IGNORE/>.\n\nContext:\n%s\n\nMessage: %s", contextHistory, transcription)
	engagementRaw, _, err := deps.Ollama.Generate("dex-engagement-model", prompt, nil)
	if err != nil {
		log.Printf("Engagement check failed: %v", err)
		return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
	}

	shouldEngage := strings.Contains(engagementRaw, "<ENGAGE/>")

	engagementReason := "Evaluated by dex-engagement-model"

	userCount, err := deps.Discord.GetVoiceChannelUserCount(channelID)
	if err != nil {
		log.Printf("Failed to get voice channel user count: %v", err)
	} else {
		log.Printf("Voice channel user count: %d", userCount)
		// Increased threshold from 2 to 3 to engage more often in small groups
		if userCount <= 3 {
			if !shouldEngage {
				log.Printf("Forcing engagement because user count is low (count: %d)", userCount)
				shouldEngage = true
				engagementReason = fmt.Sprintf("Forced engagement: User count is %d (Low population)", userCount)
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
		"engagement_model": "dex-engagement-model",
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

		// Fetch messages for Chat API
		messages, err := smartcontext.GetMessages(ctx, deps.Redis, deps.Ollama, channelID, summaryModel)
		if err != nil {
			log.Printf("Warning: Failed to fetch messages context: %v", err)
		}

		// Load Profile for Personalization
		userProfile, _ := profiler.LoadProfile(ctx, deps.Redis, userID)
		dossierPrompt := ""
		if userProfile != nil {
			dossierPrompt = fmt.Sprintf("\n\n### USER DOSSIER (Clinical Analysis):\n- Technical Level: %.1f/10\n- Comm Style: %s\n- Current Vibe: %s\n- Key Facts: ",
				userProfile.CognitiveModel.TechnicalLevel,
				userProfile.CognitiveModel.CommunicationStyle,
				userProfile.CognitiveModel.Vibe)

			facts := []string{}
			for _, attr := range userProfile.Attributes {
				facts = append(facts, fmt.Sprintf("%s: %s", attr.Key, attr.Value))
			}
			if len(facts) > 0 {
				dossierPrompt += strings.Join(facts, ", ")
			} else {
				dossierPrompt += "None observed yet."
			}
			dossierPrompt += "\nUse this dossier to personalize your tone, technical depth, and overall engagement strategy for this specific user."
		}

		systemPrompt := "You are Dexter, an advanced AI assistant. You are currently responding to a voice transcription in a Discord voice channel. Keep your responses concise and natural for voice synthesis."
		if dossierPrompt != "" {
			systemPrompt += dossierPrompt
		}

		finalMessages := []ollama.Message{
			{Role: "system", Content: systemPrompt},
		}
		finalMessages = append(finalMessages, messages...)
		// Add the current transcription if it's not already in messages (it likely isn't as the event was just created)
		finalMessages = append(finalMessages, ollama.Message{Role: "user", Content: transcription, Name: userName})

		responseModel := "dex-transcription-model"

		fullResponse := ""
		options := map[string]interface{}{
			"repeat_penalty": 1.3,
		}

		// Use Chat instead of Generate for better multi-turn awareness
		respMessage, err := deps.Ollama.ChatWithOptions(ctx, responseModel, finalMessages, options)

		if err != nil {
			log.Printf("Response generation failed: %v", err)
		} else {
			fullResponse = respMessage.Content
			log.Printf("Generated response: %s", fullResponse)

			botResponseEventData := map[string]interface{}{
				"type":           "messaging.bot.voice_response",
				"source":         "discord",
				"user_id":        "dexter-bot",
				"user_name":      "Dexter",
				"channel_id":     channelID,
				"channel_name":   channelName,
				"server_id":      serverID,
				"server_name":    serverName,
				"content":        fullResponse,
				"timestamp":      time.Now().Format(time.RFC3339),
				"response_model": responseModel,
				"response_raw":   fullResponse,
				"raw_input":      fmt.Sprintf("%v", finalMessages),
			}

			if err := emitEvent(deps.EventServiceURL, botResponseEventData); err != nil {
				log.Printf("Failed to emit bot response event: %v", err)
			}

			deps.Discord.UpdateBotStatus("Generating speech...", "online", 0)

			ttsPayload := map[string]string{"text": fullResponse, "language": "en"}
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
