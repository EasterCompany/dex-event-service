package privatemessage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/config"
	"github.com/EasterCompany/dex-event-service/internal/analysis"
	"github.com/EasterCompany/dex-event-service/internal/handlers"
	"github.com/EasterCompany/dex-event-service/internal/handlers/profiler"
	"github.com/EasterCompany/dex-event-service/internal/ollama"
	"github.com/EasterCompany/dex-event-service/internal/smartcontext"
	"github.com/EasterCompany/dex-event-service/types"
	"github.com/EasterCompany/dex-event-service/utils"
)

func emitEvent(serviceURL string, eventData map[string]interface{}) error {
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

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	content, _ := input.EventData["content"].(string)
	channelID, _ := input.EventData["channel_id"].(string)
	userID, _ := input.EventData["user_id"].(string)

	// --- 0. Reload Options Dynamically ---
	if opt, err := config.LoadOptions(); err == nil {
		deps.Options = opt
	}

	var prompt string
	var evalHistory string

	// 0. Claim Check (Anti-Race-Condition)

	// Ensure we always clear the process status when we're done
	defer utils.ClearProcess(context.Background(), deps.Redis, deps.Discord, channelID)

	var attachments []map[string]interface{}
	if att, ok := input.EventData["attachments"].([]interface{}); ok {
		for _, a := range att {
			if m, ok := a.(map[string]interface{}); ok {
				attachments = append(attachments, m)
			}
		}
	}

	urlRegex := `(https?://[^\s]+)`
	re := regexp.MustCompile(urlRegex)
	foundURLs := re.FindAllString(content, -1)

	linkContext := ""

	// 2. Check interruption before URL expansion
	if deps.CheckInterruption != nil && deps.CheckInterruption() {
		return types.HandlerOutput{Success: true}, nil
	}

	for _, foundURL := range foundURLs {
		if !strings.HasPrefix(foundURL, "https://discord.com/attachments/") {
			utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Analyzing link...")
			log.Printf("Found external URL in message: %s", foundURL)
			meta, err := deps.Web.FetchMetadata(foundURL)
			if err != nil {
				log.Printf("Failed to fetch metadata for %s: %v", foundURL, err)
				continue
			}

			var summary string
			if meta.Content != "" {
				contentToSummarize := meta.Content
				if len(contentToSummarize) > 12000 {
					contentToSummarize = contentToSummarize[:12000]
				}
				summary, _, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
				summary = strings.TrimSpace(summary)
			}
			// Check for explicit text in link metadata
			if meta.Title != "" || meta.Description != "" {
				modPrompt := fmt.Sprintf("Analyze this link metadata:\n\nTitle: %s\nDescription: %s\nURL: %s", meta.Title, meta.Description, foundURL)

				isExplicitRaw, _, err := deps.Ollama.Generate("dex-moderation-model", modPrompt, nil)
				if err == nil && strings.Contains(isExplicitRaw, "<EXPLICIT_CONTENT_DETECTED/>") {
					log.Printf("EXPLICIT LINK TEXT DETECTED: %s. Deleting message...", foundURL)

					messageID, _ := input.EventData["message_id"].(string)
					if messageID != "" {
						_ = deps.Discord.DeleteMessage(channelID, messageID)
					}

					// Automated response for user clarity in DMs
					_, _ = deps.Discord.PostMessage(channelID, "I've detected explicit content in that link and have ignored it for safety.")

					modEvent := map[string]interface{}{
						"type":         types.EventTypeModerationExplicitContentDeleted,
						"source":       "dex-event-service",
						"user_id":      userID,
						"user_name":    input.EventData["user_name"],
						"channel_id":   channelID,
						"channel_name": input.EventData["channel_name"],
						"server_id":    input.EventData["server_id"],
						"server_name":  input.EventData["server_name"],
						"timestamp":    time.Now().Format(time.RFC3339),
						"message_id":   messageID,
						"reason":       "Explicit link text detected: " + foundURL,
						"handler":      "private-message-handler",
						"raw_output":   isExplicitRaw,
					}
					_ = emitEvent(deps.EventServiceURL, modEvent)

					return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
						nil
				}
			}

			if meta.Title != "" || meta.Description != "" || summary != "" {
				linkContext += fmt.Sprintf("\n[Link: %s", foundURL)
				if meta.Title != "" {
					linkContext += fmt.Sprintf(" - Title: %s", meta.Title)
				}
				if meta.Description != "" {
					linkContext += fmt.Sprintf(" - Description: %s", meta.Description)
				}
				if summary != "" {
					linkContext += fmt.Sprintf("\n - Content Summary: %s", summary)
				}
				linkContext += "]"

				linkEvent := map[string]interface{}{
					"type":            types.EventTypeAnalysisLinkCompleted,
					"parent_event_id": input.EventID,
					"handler":         "private-message-handler",
					"url":             foundURL,
					"title":           meta.Title,
					"description":     meta.Description,
					"summary":         summary,
					"timestamp":       time.Now().Unix(),
					"channel_id":      channelID,
					"user_id":         userID,
					"server_id":       input.EventData["server_id"],
				}
				if err := emitEvent(deps.EventServiceURL, linkEvent); err != nil {
					log.Printf("Warning: Failed to emit link event: %v", err)
				}
			}

			if meta.ImageURL != "" {
				log.Printf("Expanded URL %s to image: %s (Type: %s)", foundURL, meta.ImageURL, meta.ContentType)
				ext := "jpg"
				if parts := strings.Split(meta.ContentType, "/"); len(parts) > 1 {
					ext = parts[1]
				}
				attachments = append(attachments, map[string]interface{}{
					"id":           "virtual_" + foundURL,
					"url":          meta.ImageURL,
					"content_type": meta.ContentType,
					"filename":     fmt.Sprintf("link_expansion_%s.%s", meta.Provider, ext),
					"size":         0,
					"proxy_url":    "",
					"height":       0,
					"width":        0,
				})
			}
		}
	}

	if linkContext != "" {
		content += linkContext
	}

	log.Printf("private-message-handler processing for user %s: %s (attachments: %d)", userID, content, len(attachments))

	utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Thinking...")

	visualContext := ""
	if len(attachments) > 0 {
		// 3. Check interruption before Image Analysis
		if deps.CheckInterruption != nil && deps.CheckInterruption() {
			return types.HandlerOutput{Success: true}, nil
		}

		for _, att := range attachments {
			contentType, _ := att["content_type"].(string)
			url, _ := att["url"].(string)
			filename, _ := att["filename"].(string)
			id, _ := att["id"].(string)

			if strings.HasPrefix(contentType, "image/") {
				utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, fmt.Sprintf("Analyzing Image: %s", filename))

				var description string
				cacheKey := fmt.Sprintf("analysis:visual:%s", id)
				if deps.Redis != nil {
					if cached, err := deps.Redis.Get(context.Background(), cacheKey).Result(); err == nil {
						description = cached
						log.Printf("Using cached visual description for %s", filename)
					}
				}

				if description == "" {
					log.Printf("Downloading image: %s", filename)
					base64Img, err := utils.DownloadImageAndConvertToJPEG(url)
					if err != nil {
						log.Printf("Failed to download image %s: %v", filename, err)
						continue
					}

					log.Printf("Generating visual description for %s...", filename)
					prompt := `Analyze this image for content moderation.
Rules:
1. If the image depicts hard-core pornography, realistic sexual acts, exposed genitalia designed for arousal, or "filth", output ONLY the tag <EXPLICIT_CONTENT_DETECTED/>.
2. If the image contains non-sexual nudity (classical art, statues, medical context), memes, cartoons, or is otherwise safe, provide a concise visual description.
3. DO NOT flag memes or common internet GIFs as explicit unless they depict actual sexual acts.
4. Treat screenshots of pornographic websites or links to explicit galleries as Rule 1.`
					description, _, err = deps.Ollama.Generate("dex-vision", prompt, []string{base64Img})
					if err != nil {
						log.Printf("Vision model failed for %s: %v", filename, err)
						continue
					}

					if strings.Contains(description, "<EXPLICIT_CONTENT_DETECTED/>") {
						log.Printf("EXPLICIT CONTENT DETECTED in %s. Deleting message...", filename)

						messageID, _ := input.EventData["message_id"].(string)
						if messageID != "" {
							if err := deps.Discord.DeleteMessage(channelID, messageID); err != nil {
								log.Printf("Failed to delete explicit message: %v", err)
							} else {
								log.Printf("Successfully deleted explicit message %s", messageID)
							}
						}

						// Automated response for user clarity in DMs
						_, _ = deps.Discord.PostMessage(channelID, "I've detected explicit content in that attachment and have ignored it for safety.")

						modEvent := map[string]interface{}{
							"type":         types.EventTypeModerationExplicitContentDeleted,
							"source":       "dex-event-service",
							"user_id":      userID,
							"user_name":    input.EventData["user_name"],
							"channel_id":   channelID,
							"channel_name": input.EventData["channel_name"],
							"server_id":    input.EventData["server_id"],
							"server_name":  input.EventData["server_name"],
							"timestamp":    time.Now().Format(time.RFC3339),
							"message_id":   messageID,
							"reason":       "Explicit content detected in attachment: " + filename,
							"handler":      "private-message-handler",
							"raw_output":   description,
						}
						if err := emitEvent(deps.EventServiceURL, modEvent); err != nil {
							log.Printf("Failed to emit moderation event: %v", err)
						}

						return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
					}

					if deps.Redis != nil {
						deps.Redis.Set(context.Background(), cacheKey, description, 24*time.Hour)
					}
				}

				log.Printf("Visual description for %s: %s", filename, description)
				visualContext += fmt.Sprintf("\n[Attachment: %s (Image) - Description: %s]", filename, description)

				analysisEvent := map[string]interface{}{
					"type":            "analysis.visual.completed",
					"parent_event_id": input.EventID,
					"handler":         "private-message-handler",
					"filename":        filename,
					"description":     description,
					"timestamp":       time.Now().Unix(),
					"channel_id":      channelID,
					"user_id":         userID,
					"server_id":       input.EventData["server_id"],
				}
				// Fix handler name in event
				analysisEvent["handler"] = "private-message-handler"

				if err := emitEvent(deps.EventServiceURL, analysisEvent); err != nil {
					log.Printf("Warning: Failed to emit visual event: %v", err)
				}
			}
		}
	}

	if visualContext != "" {
		content += visualContext
	}

	// 0.5. Busy Check (Single Serving AI)
	if utils.IsSystemBusy(ctx, deps.Redis, false) {
		log.Printf("System is busy with background tasks. Dexter is dipping out of this DM.")
		return types.HandlerOutput{Success: true}, nil
	}

	lockKey := fmt.Sprintf("engagement:processing:%s", channelID)
	if deps.Redis != nil {
		locked, err := deps.Redis.SetNX(context.Background(), lockKey, "1", 60*time.Second).Result()
		if err == nil && !locked {
			log.Printf("Channel %s is already being processed by another handler. Exiting to allow aggregation.", channelID)
			return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
		}
		defer deps.Redis.Del(context.Background(), lockKey)
	}

	// Dynamic Model Resolution
	uDevice := "cpu"
	uSpeed := "smart"
	if deps.Options != nil {
		uDevice = deps.Options.Ollama.UtilityDevice
		uSpeed = deps.Options.Ollama.UtilitySpeed
	}

	modelEngagement := utils.ResolveModel("engagement", uDevice, uSpeed)
	modelSummary := utils.ResolveModel("summary", uDevice, uSpeed)
	modelResponse := utils.ResolveModel("private-message", "gpu", "smart")

	// Model Options (Keep-Alive for fast-cpu utilities)
	utilityOptions := map[string]interface{}{
		"num_thread": runtime.NumCPU(),
	}
	if strings.HasSuffix(modelEngagement, "-fast-cpu") || strings.HasSuffix(modelSummary, "-fast-cpu") {
		utilityOptions["keep_alive"] = -1
	}

	shouldEngage := false
	engagementReason := "Evaluated by " + modelEngagement
	var engagementRaw string
	var err error

	// --- 1. Determine Engagement Decision ---
	evalHistory, _ = deps.Discord.FetchContext(channelID, 10)
	utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Vibe Check")
	prompt = fmt.Sprintf(`Context:
%s

Current Message:
%s

Your task is to decide how Dexter should engage with the current Private Message (DM).
Output EXACTLY one of the following tokens:
- "IGNORE": No action.
- "REACTION:<emoji>": React with a single emoji.
- "ENGAGE": Generate a full text response.

Output ONLY the token.`, evalHistory, content)

	engagementRaw, _, err = deps.Ollama.GenerateWithContext(ctx, modelEngagement, prompt, nil, utilityOptions)
	decisionStr := "IGNORE"

	if err == nil {
		engagementRaw = strings.TrimSpace(engagementRaw)
		upperRaw := strings.ToUpper(engagementRaw)

		if strings.Contains(upperRaw, "ENGAGE") || strings.Contains(upperRaw, "<ENGAGE/>") {
			shouldEngage = true
			decisionStr = "REPLY_REGULAR"
		} else if strings.Contains(upperRaw, "REACTION:") || strings.Contains(upperRaw, "REACT:") {
			shouldEngage = false
			decisionStr = "REACTION"
			// Extract emoji from either REACTION: or REACT:
			parts := strings.SplitN(engagementRaw, ":", 2)
			if len(parts) == 2 {
				emoji := strings.TrimSpace(parts[1])
				if emoji != "" {
					messageID, _ := input.EventData["message_id"].(string)
					if messageID != "" {
						log.Printf("Reacting with emoji in DM %s: %s", channelID, emoji)
						_ = deps.Discord.AddReaction(channelID, messageID, emoji)
						decisionStr = "REACTION:" + emoji
					}
				}
			}
		} else {
			shouldEngage = false
			decisionStr = "IGNORE"
		}
	} else {
		// Even if model fails, populate telemetry for the event
		evalHistory, _ = deps.Discord.FetchContext(channelID, 10)
		engagementRaw = "ERROR: " + err.Error()
	}

	responseModel := modelResponse

	// Fetch Context (Hybrid Lazy Summary) - Disable automatic background summarization during thinking phase
	contextLimit := 24000
	messages, contextEventIDs, err := smartcontext.GetMessages(ctx, deps.Redis, deps.Ollama, channelID, "", contextLimit, utilityOptions)
	if err != nil {
		log.Printf("Warning: Failed to fetch messages context: %v", err)
	}

	// Fetch channel members for normalization
	var userMap map[string]string // username -> ID
	users, _, err := deps.Discord.FetchChannelMembers(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch channel members: %v", err)
	} else {
		userMap = make(map[string]string)
		for _, u := range users {
			userMap[u.Username] = u.ID
		}

		if mentions, ok := input.EventData["mentions"].([]interface{}); ok {
			content = utils.NormalizeMentions(content, mentions)
		}
	}

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         decisionStr,
		"reason":           engagementReason,
		"handler":          "private-message-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  content,
		"timestamp":        time.Now().Unix(),
		"engagement_model": modelEngagement,
		"message_count":    len(messages),
		"engagement_raw":   engagementRaw,
		"input_prompt":     prompt,
		"context_history":  evalHistory,
	}

	if err := emitEvent(deps.EventServiceURL, engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	if shouldEngage {
		// 1.5. Check Fabricator Intent (STRICT COMMAND MODE)
		userName, _ := input.EventData["user_name"].(string)
		lowerContent := strings.ToLower(content)
		isCommand := strings.HasPrefix(lowerContent, "!build") || strings.HasPrefix(lowerContent, "!fabricate")

		if isCommand {
			var cleanContent string
			if strings.HasPrefix(lowerContent, "!build") {
				cleanContent = strings.TrimSpace(content[6:])
			} else {
				cleanContent = strings.TrimSpace(content[10:])
			}

			// DMs are always "mentionedBot" = true effectively, but we pass true to satisfy logic if needed
			// Pass evalHistory to provide short-term memory to the Fabricator
			isFabricator, err := handlers.HandleFabricatorIntent(ctx, cleanContent, userID, userName, channelID, input.EventData["server_id"].(string), true, false, true, evalHistory, deps)
			if err == nil && isFabricator {
				return types.HandlerOutput{Success: true}, nil
			}
		}

		// Race Condition Protection: Claim all events in the current context
		if deps.Redis != nil {
			for _, id := range contextEventIDs {
				deps.Redis.Set(ctx, "handled:event:"+id, "1", 2*time.Minute)
			}
			// Also claim the current triggering event
			deps.Redis.Set(ctx, "handled:event:"+input.EventID, "1", 2*time.Minute)
		}

		// 4. Final interruption check before generation
		if deps.CheckInterruption != nil && deps.CheckInterruption() {
			return types.HandlerOutput{Success: true}, nil
		}

		utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Thinking...")
		deps.Discord.TriggerTyping(channelID)

		// VRAM Optimization
		_ = deps.Ollama.UnloadAllModelsExcept(ctx, responseModel)

		if deps.Redis != nil {
			deps.Redis.Expire(context.Background(), lockKey, 60*time.Second)
		}

		// --- Load Profile for Personalization ---
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

		systemPrompt := utils.GetBaseSystemPrompt() + dossierPrompt

		// Final construction of chat messages
		finalMessages := []ollama.Message{
			{Role: "system", Content: systemPrompt},
		}
		finalMessages = append(finalMessages, messages...)
		finalMessages = append(finalMessages, ollama.Message{Role: "user", Content: content})

		streamMessageID, err := deps.Discord.InitStream(channelID, utils.GetLoadingMessage())
		if err != nil {
			log.Printf("Failed to init stream: %v", err)
			return types.HandlerOutput{Success: false, Error: err.Error()}, err
		}

		fullResponse := ""
		options := map[string]interface{}{
			"repeat_penalty": 1.3,
		}
		stats, err := deps.Ollama.ChatStream(ctx, responseModel, finalMessages, options, func(chunk string) {
			fullResponse += chunk

			denormalizedResponse := fullResponse
			if len(userMap) > 0 {
				denormalizedResponse = utils.DenormalizeMentions(fullResponse, userMap)
			}
			deps.Discord.UpdateStream(channelID, streamMessageID, denormalizedResponse)
		})
		if err != nil {
			log.Printf("Response generation failed: %v", err)
			_, _ = deps.Discord.CompleteStream(channelID, streamMessageID, "Error: I couldn't generate a response. Please try again later.")
			return types.HandlerOutput{Success: false, Error: err.Error()}, err
		}

		finalResponse := fullResponse
		if len(userMap) > 0 {
			finalResponse = utils.DenormalizeMentions(fullResponse, userMap)
		}
		finalMessageID, _ := deps.Discord.CompleteStream(channelID, streamMessageID, finalResponse)
		log.Printf("Generated response: %s", fullResponse)

		// Build chat history for audit event
		var chatHistory []map[string]string
		for _, m := range finalMessages {
			chatHistory = append(chatHistory, map[string]string{"role": m.Role, "content": m.Content, "name": m.Name})
		}
		chatHistory = append(chatHistory, map[string]string{"role": "assistant", "content": fullResponse, "name": "Dexter"})

		botEventData := map[string]interface{}{
			"type":               types.EventTypeMessagingBotSentMessage,
			"source":             "dex-event-service",
			"user_id":            "dexter",
			"user_name":          "Dexter",
			"target_user_id":     userID,
			"channel_id":         channelID,
			"channel_name":       "DM",
			"server_id":          input.EventData["server_id"],
			"server_name":        "DM",
			"message_id":         finalMessageID,
			"content":            fullResponse,
			"timestamp":          time.Now().Format(time.RFC3339),
			"response_model":     responseModel,
			"response_raw":       fullResponse,
			"raw_input":          fmt.Sprintf("%v", finalMessages), // Store representation of structured input
			"chat_history":       chatHistory,
			"eval_count":         stats.EvalCount,
			"prompt_count":       stats.PromptEvalCount,
			"duration_ms":        stats.TotalDuration.Milliseconds(),
			"load_duration_ms":   stats.LoadDuration.Milliseconds(),
			"prompt_duration_ms": stats.PromptEvalDuration.Milliseconds(),
			"eval_duration_ms":   stats.EvalDuration.Milliseconds(),
		}
		if err := emitEvent(deps.EventServiceURL, botEventData); err != nil {
			log.Printf("Warning: Failed to emit event: %v", err)
		}

		// --- Async Tasks (Housekeeping & Profiling) ---
		go func() {
			analysisModel := modelSummary

			// 1. Context Housekeeping (Summarize if needed)
			smartcontext.UpdateSummary(context.Background(), deps.Redis, deps.Ollama, channelID, analysisModel, smartcontext.CachedSummary{}, nil, utilityOptions)

			// 2. Signal Extraction for Profiling
			// For analysis we still use text block
			historyText := ""
			for _, m := range messages {
				historyText += fmt.Sprintf("%s: %s\n", m.Role, m.Content)
			}
			signals, raw, err := analysis.ExtractWithContext(context.Background(), deps.Ollama, analysisModel, content, historyText, utilityOptions)
			if err != nil {
				log.Printf("Signal extraction failed: %v", err)
				return
			}

			signalEvent := map[string]interface{}{
				"type":            "analysis.user.message_signals",
				"parent_event_id": input.EventID,
				"user_id":         userID,
				"channel_id":      channelID,
				"signals":         signals,
				"raw_output":      raw,
				"model":           analysisModel,
				"timestamp":       time.Now().Unix(),
			}
			_ = emitEvent(deps.EventServiceURL, signalEvent)
		}()
	}

	return types.HandlerOutput{
		Success: true,
		Events:  []types.HandlerOutputEvent{},
	}, nil
}
