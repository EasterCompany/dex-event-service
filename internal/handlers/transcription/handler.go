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
	"sync"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/internal/handlers/profiler"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
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

	// 0. Claim Check (Anti-Race-Condition)
	// If this event was already included in a previous response's context window, skip it.
	if claimed, _ := deps.Redis.Get(ctx, "handled:event:"+input.EventID).Result(); claimed == "1" {
		log.Printf("Event %s already handled by previous cycle. Skipping.", input.EventID)
		return types.HandlerOutput{Success: true}, nil
	}

	log.Printf("transcription-handler processing for user %s: %s", userName, transcription)

	// 0.5 Cooldown Check (Anti-Double-Response)
	// If we just finished speaking, ignore inputs for a short window (2s) to prevent responding to self-echo or late fragments.
	lastResponseKey := fmt.Sprintf("channel:voice_cooldown:%s", channelID)
	if ttl, _ := deps.Redis.TTL(ctx, lastResponseKey).Result(); ttl > 0 {
		log.Printf("Ignoring transcription due to voice cooldown (TTL: %v)", ttl)
		return types.HandlerOutput{Success: true}, nil
	}

	// 0.6 Busy Check (Single Serving AI)
	if utils.IsSystemBusy(ctx, deps.Redis, true) {
		log.Printf("System is busy with background tasks. Dexter is dipping out of this voice conversation.")
		return types.HandlerOutput{Success: true}, nil
	}

	deps.Discord.UpdateBotStatus("Thinking...", "online", 3)
	defer deps.Discord.UpdateBotStatus("Listening for events...", "online", 2)

	// Fetch context
	summaryModel := "dex-summary"
	contextLimit := 12000
	contextHistory, err := smartcontext.Get(ctx, deps.Redis, deps.Ollama, channelID, summaryModel, contextLimit)
	if err != nil {
		log.Printf("Warning: Failed to fetch context from smartcontext: %v, falling back to legacy", err)
		contextHistory, _ = deps.Discord.FetchContext(channelID, 25)
	}

	// VRAM Optimization
	// Skip unloading if we hold the Voice Mode lock, as we want both models loaded.
	holder, _ := deps.Redis.Get(ctx, "system:cognitive_lock").Result()
	if holder != "Voice Mode" {
		_ = deps.Ollama.UnloadAllModelsExcept(ctx, "dex-engagement-fast")
	}

	// 1. Check Engagement
	shouldEngage := false
	engagementRaw := "N/A (Skipped)"
	engagementReason := ""

	userCount, err := deps.Discord.GetVoiceChannelUserCount(channelID)
	if err != nil {
		log.Printf("Failed to get voice channel user count: %v", err)
		userCount = 0 // Assume unknown
	}

	// Optimization: If it's a 1-on-1 (just user and Dexter), FORCE engagement without inference.
	if userCount > 0 && userCount <= 2 {
		log.Printf("Forcing engagement because user count is low (count: %d). Skipping inference.", userCount)
		shouldEngage = true
		engagementReason = fmt.Sprintf("Forced engagement: User count is %d (Low population)", userCount)
	} else {
		// More users present: Perform inference to see if Dexter was addressed.
		prompt := fmt.Sprintf("Analyze if Dexter should respond to this message (from voice transcription). Output <ENGAGE/> or <IGNORE/>.\n\nContext:\n%s\n\nMessage: %s", contextHistory, transcription)
		engagementRaw, _, err = deps.Ollama.Generate("dex-engagement-fast", prompt, nil)
		if err != nil {
			log.Printf("Engagement check failed: %v", err)
			return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
		}
		shouldEngage = strings.Contains(engagementRaw, "<ENGAGE/>")
		engagementReason = "Evaluated by dex-engagement-fast"
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
		"engagement_model": "dex-engagement-fast",
		"context_history":  contextHistory,
		"engagement_raw":   engagementRaw,
		"user_count":       userCount,
	}

	if err := emitEvent(deps.EventServiceURL, engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	if shouldEngage {
		// Fetch messages for Chat API
		messages, contextEventIDs, err := smartcontext.GetMessages(ctx, deps.Redis, deps.Ollama, channelID, summaryModel, contextLimit, nil)
		if err != nil {
			log.Printf("Warning: Failed to fetch messages context: %v", err)
		}

		// Race Condition Protection: Claim all events in the current context
		if deps.Redis != nil {
			for _, id := range contextEventIDs {
				deps.Redis.Set(ctx, "handled:event:"+id, "1", 2*time.Minute)
			}
			// Also claim the current triggering event
			deps.Redis.Set(ctx, "handled:event:"+input.EventID, "1", 2*time.Minute)
		}

		deps.Discord.UpdateBotStatus("Thinking of response...", "online", 0)

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

		responseModel := "dex-transcription"

		// VRAM Optimization
		if holder != "Voice Mode" {
			_ = deps.Ollama.UnloadAllModelsExcept(ctx, responseModel)
		}

		fullResponse := ""
		options := map[string]interface{}{
			"repeat_penalty": 1.3,
		}

		// Use ChatStream for lower latency voice response
		sentenceChan := make(chan string, 10)
		audioChan := make(chan []byte, 10)
		var wg sync.WaitGroup

		// 1. Player Worker (Consumes Audio -> Plays to Discord)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for audioData := range audioChan {
				deps.Discord.UpdateBotStatus("Speaking...", "online", 0)
				if playErr := deps.Discord.PlayAudio(audioData); playErr != nil {
					log.Printf("Failed to play audio in Discord: %v", playErr)
				}
			}
		}()

		// 2. TTS Worker (Consumes Sentences -> Generates Audio)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for text := range sentenceChan {
				log.Printf("Generating TTS for: %s", text)
				deps.Discord.UpdateBotStatus("Generating speech...", "online", 0)

				ttsPayload := map[string]string{"text": text, "language": "en"}
				ttsJson, _ := json.Marshal(ttsPayload)

				ttsResp, ttsErr := http.Post(deps.TTSServiceURL+"/generate", "application/json", bytes.NewBuffer(ttsJson))
				if ttsErr != nil {
					log.Printf("TTS generation failed: %v", ttsErr)
					continue
				}

				if ttsResp.StatusCode == 200 {
					audioBytes, _ := io.ReadAll(ttsResp.Body)
					_ = ttsResp.Body.Close()
					audioChan <- audioBytes
				} else {
					_ = ttsResp.Body.Close()
					log.Printf("TTS service error: %d", ttsResp.StatusCode)
				}
			}
			close(audioChan)
		}()

		// 3. LLM Streamer
		fullResponseBuilder := strings.Builder{}
		currentBuffer := strings.Builder{}

		_, err = deps.Ollama.ChatStream(ctx, responseModel, finalMessages, options, func(token string) {
			fullResponseBuilder.WriteString(token)

			currentBuffer.WriteString(token)

			// Check for sentence delimiters
			// Simple heuristic: . ? ! followed by space or newline, or just newline
			// We optimize for speed, so splitting on simple punctuation is key.
			buff := currentBuffer.String()

			// If buffer is too short, keep accumulating (avoid splitting "Mr." or "A.I.")
			if len(buff) < 10 {
				return
			}

			// Check for delimiter
			hasDelimiter := false
			if strings.ContainsAny(buff, ".?!") {
				// Only split if we have a delimiter AND it's at the end or followed by space
				// Actually, since we receive token by token, the current token might be the delimiter.
				// We look for the *last* delimiter index.
				lastIdx := strings.LastIndexAny(buff, ".?!")
				if lastIdx != -1 && lastIdx < len(buff) {
					// We have a potential sentence end.
					// To be safe, we check if it looks like an abbreviation or we should wait for more context?
					// For v1, aggressive splitting is better for latency.
					// We take everything up to the delimiter as the sentence.

					sentence := buff[:lastIdx+1]
					remainder := buff[lastIdx+1:]

					sentenceChan <- strings.TrimSpace(sentence)
					currentBuffer.Reset()
					currentBuffer.WriteString(remainder)
					hasDelimiter = true
				}
			}

			// Also split on newlines
			if !hasDelimiter && strings.Contains(buff, "\n") {
				parts := strings.Split(buff, "\n")
				// Send all complete parts
				for i := 0; i < len(parts)-1; i++ {
					if trimmed := strings.TrimSpace(parts[i]); trimmed != "" {
						sentenceChan <- trimmed
					}
				}
				// Keep the last part
				currentBuffer.Reset()
				currentBuffer.WriteString(parts[len(parts)-1])
			}
		})

		// Flush remaining buffer
		if currentBuffer.Len() > 0 {
			if trimmed := strings.TrimSpace(currentBuffer.String()); trimmed != "" {
				sentenceChan <- trimmed
			}
		}

		close(sentenceChan) // Signal TTS worker to finish
		wg.Wait()           // Wait for TTS and Player to finish

		fullResponse = fullResponseBuilder.String()

		if err != nil {
			log.Printf("Response generation failed: %v", err)
		} else {
			log.Printf("Generated full response: %s", fullResponse)

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

			// Set cooldown to prevent immediate re-triggering
			deps.Redis.Set(ctx, fmt.Sprintf("channel:voice_cooldown:%s", channelID), "1", 2*time.Second)
		}
	}

	return types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}, nil
}
