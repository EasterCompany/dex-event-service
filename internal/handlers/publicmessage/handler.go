package publicmessage

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
	"github.com/EasterCompany/dex-event-service/internal/web"
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
	mentionedBot, _ := input.EventData["mentioned_bot"].(bool)

	// --- 0. Reload Options Dynamically ---
	// We reload here so that model resolution and quiet mode use the latest user settings.
	if opt, err := config.LoadOptions(); err == nil {
		deps.Options = opt
	}

	// 0. Claim Check (Anti-Race-Condition)
	// If this event was already included in a previous response's context window, skip it.
	if deps.Redis != nil {
		if claimed, _ := deps.Redis.Get(ctx, "handled:event:"+input.EventID).Result(); claimed == "1" {
			log.Printf("Event %s already handled by previous cycle. Skipping.", input.EventID)
			return types.HandlerOutput{Success: true}, nil
		}
	}

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

	// Prepare prompt for router model (used for each URL decision)
	routerBasePrompt := fmt.Sprintf("User message: \"%s\"", content)

	for _, foundURL := range foundURLs {
		if !strings.HasPrefix(foundURL, "https://discord.com/attachments/") {
			log.Printf("Found external URL in message: %s", foundURL)

			// --- Determine Fetch Method using dex-router-model ---

			decision := "STATIC" // Default to static
			routerInput := fmt.Sprintf("%s\nURL to analyze: %s", routerBasePrompt, foundURL)
			routerOutput, _, err := deps.Ollama.Generate("dex-router-model", routerInput, nil)
			if err != nil {
				log.Printf("dex-router-model failed: %v, defaulting to STATIC", err)
			} else {
				if strings.Contains(routerOutput, "<VISUAL/>") {
					decision = "VISUAL"
				} else {
					decision = "STATIC"
				}

				routerEvent := map[string]interface{}{
					"type":            types.EventTypeAnalysisRouterDecision,
					"handler":         "public-message-handler",
					"parent_event_id": input.EventID,
					"url":             foundURL,
					"decision":        decision,
					"raw_output":      routerOutput,
					"raw_input":       routerInput,
					"model":           "dex-router-model",
					"timestamp":       time.Now().Unix(),
					"channel_id":      channelID,
					"user_id":         userID,
				}
				if err := emitEvent(deps.EventServiceURL, routerEvent); err != nil {
					log.Printf("Warning: Failed to emit router decision event: %v", err)
				}
			}
			// --- End Router Model Logic ---

			var meta *web.MetadataResponse
			var webView *web.WebViewResponse
			var fetchErr error

			if decision == "VISUAL" {
				utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Viewing link...")
				webView, fetchErr = deps.Web.FetchWebView(foundURL)
				if fetchErr == nil && webView != nil {
					if err := utils.StoreWebHistory(deps.Redis, webView, foundURL); err != nil {
						log.Printf("Failed to store web history: %v", err)
					}
				}
				if fetchErr != nil {
					log.Printf("Failed to fetch web view for %s: %v, falling back to STATIC", foundURL, fetchErr)
					// Fallback to static if webview fails
					utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Analyzing link...")
					meta, fetchErr = deps.Web.FetchMetadata(foundURL)
				}
			} else {
				utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Analyzing link...")
				meta, fetchErr = deps.Web.FetchMetadata(foundURL)
			}

			if fetchErr != nil {
				log.Printf("Failed to fetch data for %s: %v", foundURL, fetchErr)
				continue
			}

			// Consolidate data for linkContext and event emission
			var currentTitle, currentDescription, currentSummary, currentImageURL, currentContentType, currentProvider string

			if webView != nil {
				currentTitle = webView.Title
				currentDescription = ""          // Webview content is too large for general description in linkEvent
				currentImageURL = ""             // Screenshot is handled separately
				currentContentType = "text/html" // General type for rendered content
				currentProvider = "webview"      // Indicate source

				// Generate summary from rendered HTML
				if webView.Content != "" {
					contentToSummarize := webView.Content
					if len(contentToSummarize) > 12000 { // Max summary length
						contentToSummarize = contentToSummarize[:12000]
					}
					currentSummary, _, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
					currentSummary = strings.TrimSpace(currentSummary)
				}

				// Add screenshot as virtual attachment
				if webView.Screenshot != "" {
					attachments = append(attachments, map[string]interface{}{
						"id":           fmt.Sprintf("virtual_screenshot_%s", input.EventID), // Unique ID for this screenshot
						"url":          foundURL,                                            // Original URL as source
						"content_type": "image/png",                                         // Assuming PNG screenshot
						"filename":     fmt.Sprintf("webview_screenshot_%s.png", input.EventID),
						"size":         0,                  // Size is not easily known without decoding, set to 0
						"proxy_url":    "",                 // Not applicable
						"height":       0,                  // Not easily known, set to 0
						"width":        0,                  // Not easily known, set to 0
						"base64":       webView.Screenshot, // Store base64 for processing by vision model
					})
				}

			} else if meta != nil { // Fallback to static metadata
				currentTitle = meta.Title
				currentDescription = meta.Description
				currentImageURL = meta.ImageURL
				currentContentType = meta.ContentType
				currentProvider = meta.Provider

				// Generate summary from static content
				if meta.Content != "" {
					contentToSummarize := meta.Content
					if len(contentToSummarize) > 12000 {
						contentToSummarize = contentToSummarize[:12000]
					}
					currentSummary, _, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
					currentSummary = strings.TrimSpace(currentSummary)
				}
			}

			// Check for explicit text in link metadata
			if currentTitle != "" || currentDescription != "" {
				modPrompt := fmt.Sprintf("Analyze this link metadata:\n\nTitle: %s\nDescription: %s\nURL: %s", currentTitle, currentDescription, foundURL)

				isExplicitRaw, _, err := deps.Ollama.Generate("dex-moderation-model", modPrompt, nil)
				if err == nil && strings.Contains(isExplicitRaw, "<EXPLICIT_CONTENT_DETECTED/>") {
					log.Printf("EXPLICIT LINK TEXT DETECTED: %s. Deleting message...", foundURL)

					messageID, _ := input.EventData["message_id"].(string)
					if messageID != "" {
						_ = deps.Discord.DeleteMessage(channelID, messageID)
					}

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
						"handler":      "public-message-handler",
						"raw_output":   isExplicitRaw,
					}
					_ = emitEvent(deps.EventServiceURL, modEvent)

					return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
						nil
				}
			}

			if currentTitle != "" || currentDescription != "" || currentSummary != "" {
				linkContext += fmt.Sprintf("\n[Link: %s", foundURL)
				if currentTitle != "" {
					linkContext += fmt.Sprintf(" - Title: %s", currentTitle)
				}
				if currentDescription != "" {
					linkContext += fmt.Sprintf(" - Description: %s", currentDescription)
				}
				if currentSummary != "" {
					linkContext += fmt.Sprintf("\n - Content Summary: %s", currentSummary)
				}
				linkContext += "]"

				linkEvent := map[string]interface{}{
					"type":            types.EventTypeAnalysisLinkCompleted,
					"parent_event_id": input.EventID,
					"handler":         "public-message-handler",
					"url":             foundURL,
					"title":           currentTitle,
					"description":     currentDescription,
					"summary":         currentSummary,
					"timestamp":       time.Now().Unix(),
					"channel_id":      channelID,
					"user_id":         userID,
					"server_id":       input.EventData["server_id"],
				}
				if err := emitEvent(deps.EventServiceURL, linkEvent); err != nil {
					log.Printf("Warning: Failed to emit link event: %v", err)
				}
			}

			// Add image URL from static metadata as virtual attachment if no webview screenshot
			if currentImageURL != "" && webView == nil {
				log.Printf("Expanded URL %s to image: %s (Type: %s)", foundURL, currentImageURL, currentContentType)
				ext := "jpg"
				if parts := strings.Split(currentContentType, "/"); len(parts) > 1 {
					ext = parts[1]
				}
				attachments = append(attachments, map[string]interface{}{
					"id":           "virtual_" + foundURL,
					"url":          currentImageURL,
					"content_type": currentContentType,
					"filename":     fmt.Sprintf("link_expansion_%s.%s", currentProvider, ext),
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

	// 0.5. Busy Check (Single Serving AI)
	if utils.IsSystemBusy(ctx, deps.Redis) {
		log.Printf("System is busy with background tasks. Dexter is dipping out of this group chat.")
		return types.HandlerOutput{Success: true}, nil
	}

	// Music Logic: Check for YouTube links in music channel or if bot is mentioned
	if channelID == "1437617331529580614" || mentionedBot {
		for _, foundURL := range foundURLs {
			if strings.Contains(foundURL, "youtube.com") || strings.Contains(foundURL, "youtu.be") {
				log.Printf("Music request detected: %s", foundURL)
				utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Playing music...")
				if err := deps.Discord.PlayMusic(foundURL); err != nil {
					log.Printf("Failed to play music: %v", err)
				}
				// If it was the music channel, we stop here (no engagement needed)
				if channelID == "1437617331529580614" {
					utils.ClearProcess(context.Background(), deps.Redis, deps.Discord, channelID)
					return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
						nil
				}
				break // Only play first link
			}
		}
	}

	log.Printf("public-message-handler processing for user %s in channel %s: %s (mentioned: %v, attachments: %d)", userID, channelID, content, mentionedBot, len(attachments))

	// Register process for dashboard visibility and sync Discord status
	utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Thinking...")

	visualContext := ""
	if len(attachments) > 0 {
		for _, att := range attachments {
			contentType, _ := att["content_type"].(string)
			url, _ := att["url"].(string)
			filename, _ := att["filename"].(string)
			id, _ := att["id"].(string)
			base64Data, hasBase64 := att["base64"].(string) // New: Check for base64 data

			if strings.HasPrefix(contentType, "image/") {
				utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, fmt.Sprintf("Analyzing Image: %s", filename))

				var description string
				var base64Img string // Hoisted declaration

				cacheKey := fmt.Sprintf("analysis:visual:%s", id)
				if deps.Redis != nil {
					if cached, err := deps.Redis.Get(context.Background(), cacheKey).Result(); err == nil {
						description = cached
						log.Printf("Using cached visual description for %s", filename)
					}
				}

				if description == "" {
					var imgErr error // Declare imgErr here

					if hasBase64 { // Use base64 from attachment directly
						base64Img = base64Data
						log.Printf("Using base64 image data for %s", filename)
					} else { // Download image from URL
						log.Printf("Downloading image: %s", filename)
						base64Img, imgErr = utils.DownloadImageAndConvertToJPEG(url)
					}

					if imgErr != nil {
						log.Printf("Failed to get image data for %s: %v", filename, imgErr)
						continue
					}

					log.Printf("Generating visual description for %s...", filename)
					prompt := `Analyze this image for content moderation.
Rules:
1. If the image depicts hard-core pornography, realistic sexual acts, exposed genitalia designed for arousal, or "filth", output ONLY the tag <EXPLICIT_CONTENT_DETECTED/>.
2. If the image contains non-sexual nudity (classical art, statues, medical context), memes, cartoons, or is otherwise safe, provide a concise visual description.
3. DO NOT flag memes or common internet GIFs as explicit unless they depict actual sexual acts.
4. Treat screenshots of pornographic websites or links to explicit galleries as Rule 1.`
					var err error
					description, _, err = deps.Ollama.Generate("dex-vision-model", prompt, []string{base64Img})
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
							"handler":      "public-message-handler",
							"raw_output":   description,
						}
						if err := emitEvent(deps.EventServiceURL, modEvent); err != nil {
							log.Printf("Failed to emit moderation event: %v", err)
						}

						return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
							nil
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
					"handler":         "public-message-handler",
					"filename":        filename,
					"description":     description,
					"timestamp":       time.Now().Unix(),
					"channel_id":      channelID,
					"user_id":         userID,
					"server_id":       input.EventData["server_id"],
					"url":             url,
					"base64_preview":  base64Img,
				}
				if err := emitEvent(deps.EventServiceURL, analysisEvent); err != nil {
					log.Printf("Warning: Failed to emit visual event: %v", err)
				}
			}
		}
	}

	if visualContext != "" {
		content += visualContext
	}

	lockKey := fmt.Sprintf("engagement:processing:%s", channelID)
	if deps.Redis != nil {
		locked, err := deps.Redis.SetNX(context.Background(), lockKey, "1", 60*time.Second).Result()
		if err == nil && !locked {
			log.Printf("Channel %s is already being processed by another handler. Exiting to allow aggregation.", channelID)
			return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
				nil
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
	modelResponse := utils.ResolveModel("public-message", "gpu", "smart") // Response always smart/gpu by default

	// Model Options (Keep-Alive for fast-cpu utilities)
	utilityOptions := map[string]interface{}{
		"num_thread": runtime.NumCPU(),
	}
	if strings.HasSuffix(modelEngagement, "-fast-cpu") || strings.HasSuffix(modelSummary, "-fast-cpu") {
		utilityOptions["keep_alive"] = -1
	}

	var decisionStr string
	responseModel := modelResponse
	var prompt string
	var evalHistory string

	shouldEngage := false
	engagementReason := "Evaluated by " + modelEngagement
	var engagementRaw string
	var err error

	// --- 1. First Determine the Engagement Decision & Model ---
	const MainChatChannelID = "1426957003108122656"

	restrictedChannels := map[string]bool{
		"1437617331529580614": true, // Music
		"1381915374181810236": true, // Memes
	}

	// --- 0. Check for Quiet Mode ---
	quietMode := false
	if deps.Options != nil {
		quietMode = deps.Options.Discord.QuietMode
	}

	if quietMode && !mentionedBot {
		log.Printf("Quiet mode is ENABLED. Ignoring unsolicited message in channel %s.", channelID)
		shouldEngage = false
		decisionStr = "IGNORE"
		engagementReason = "Quiet Mode Enabled (No Direct Mention)"
	} else if mentionedBot {
		log.Printf("Bot was mentioned, forcing engagement.")
		shouldEngage = true
		decisionStr = "REPLY"
		engagementReason = "Direct mention"
		responseModel = "dex-public-message-model"

		// Still fetch context for telemetry and event completeness
		evalHistory, _ = deps.Discord.FetchContext(channelID, 25)
		prompt = "FORCED_BY_MENTION"
		engagementRaw = "<MENTION_DETECTED/>"
	} else if restrictedChannels[channelID] {
		log.Printf("Channel %s is restricted (analysis only). Skipping engagement check.", channelID)
		shouldEngage = false
		decisionStr = "NONE"
		engagementReason = "Restricted Channel (Analysis Only)"
	} else if channelID == MainChatChannelID {
		// Fetch standard history for evaluating strategy
		evalHistory, _ = deps.Discord.FetchContext(channelID, 25)

		utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Evaluating Strategy")
		prompt = fmt.Sprintf(`Context:
%s

Current Message:
%s

Your task is to decide how Dexter should engage with the current message in the Main Chat channel.
Output EXACTLY one of the following tokens:
- "IGNORE": No action.
- "REACTION:<emoji>": React with a single emoji.
- "ENGAGE_FAST": Quick response for simple banter/social.
- "ENGAGE": Full response for complex topics.

Output ONLY the token.`, evalHistory, content)

		engagementRaw, _, err = deps.Ollama.GenerateWithContext(ctx, modelEngagement, prompt, nil, utilityOptions)
		if err != nil {
			log.Printf("Regular engagement check failed: %v, defaulting to IGNORE", err)
			decisionStr = "NONE"
		} else {
			engagementRaw = strings.TrimSpace(engagementRaw)
			upperRaw := strings.ToUpper(engagementRaw)

			if strings.Contains(upperRaw, "ENGAGE_REGULAR") || strings.Contains(upperRaw, "ENGAGE") || strings.Contains(upperRaw, "<ENGAGE/>") {
				shouldEngage = true
				decisionStr = "ENGAGE_REGULAR"
				responseModel = modelResponse
			} else if strings.Contains(upperRaw, "ENGAGE_FAST") {
				shouldEngage = true
				decisionStr = "ENGAGE_FAST"
				responseModel = modelResponse
			} else if strings.Contains(upperRaw, "REACTION:") || strings.Contains(upperRaw, "REACT:") {
				shouldEngage = false
				decisionStr = "REACTION"
				parts := strings.SplitN(engagementRaw, ":", 2)
				if len(parts) == 2 {
					emoji := strings.TrimSpace(parts[1])
					if emoji != "" {
						messageID, _ := input.EventData["message_id"].(string)
						if messageID != "" {
							log.Printf("Reacting with emoji: %s", emoji)
							_ = deps.Discord.AddReaction(channelID, messageID, emoji)
							decisionStr = "REACTION:" + emoji
						}
					}
				}
			} else {
				shouldEngage = false
				decisionStr = "IGNORE"
			}
		}
	} else {
		// --- OTHER CHANNELS: Binary Engagement + Reaction Support ---
		evalHistory, _ = deps.Discord.FetchContext(channelID, 10)
		utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Vibe Check")
		prompt = fmt.Sprintf(`Context:
%s

Current Message:
%s

Analyze if Dexter should respond to this message. 
Output EXACTLY one of the following tokens:
- "IGNORE": No action.
- "REACTION:<emoji>": React with a single emoji.
- "ENGAGE": Generate a full text response.

Output ONLY the token.`, evalHistory, content)

		engagementRaw, _, err = deps.Ollama.GenerateWithContext(ctx, modelEngagement, prompt, nil, utilityOptions)
		if err == nil {
			engagementRaw = strings.TrimSpace(engagementRaw)
			upperRaw := strings.ToUpper(engagementRaw)

			if strings.Contains(upperRaw, "ENGAGE") || strings.Contains(upperRaw, "<ENGAGE/>") {
				shouldEngage = true
				decisionStr = "ENGAGE_FAST"
				responseModel = modelResponse
			} else if strings.Contains(upperRaw, "REACTION:") || strings.Contains(upperRaw, "REACT:") {
				shouldEngage = false
				decisionStr = "REACTION"
				parts := strings.SplitN(engagementRaw, ":", 2)
				if len(parts) == 2 {
					emoji := strings.TrimSpace(parts[1])
					if emoji != "" {
						messageID, _ := input.EventData["message_id"].(string)
						if messageID != "" {
							log.Printf("Reacting with emoji in channel %s: %s", channelID, emoji)
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
			shouldEngage = false
			decisionStr = "IGNORE"
		}
	}

	// --- 2. Build Response Context based on Model Choice ---
	systemPrompt := utils.GetBaseSystemPrompt()
	summaryModel := modelSummary

	messages, contextEventIDs, err := smartcontext.GetMessages(ctx, deps.Redis, deps.Ollama, channelID, summaryModel, utilityOptions)
	if err != nil {
		log.Printf("Warning: Failed to fetch messages context: %v", err)
	}

	// Fetch channel members (active users) and build user map
	var userMap map[string]string // username -> ID
	var channelMembers string

	users, membersStr, err := deps.Discord.FetchChannelMembers(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch channel members: %v", err)
	} else {
		channelMembers = membersStr
		userMap = make(map[string]string)
		for _, u := range users {
			userMap[u.Username] = u.ID
		}

		if mentions, ok := input.EventData["mentions"].([]interface{}); ok {
			content = utils.NormalizeMentions(content, mentions)
		}
	}

	if channelMembers != "" { // Only provide detailed member list to regular models
		systemPrompt += "\n\n" + channelMembers
	}

	_ = engagementReason // Ensure variable is used

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         decisionStr,
		"reason":           "Evaluated by " + modelEngagement,
		"handler":          "public-message-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  content,
		"timestamp":        time.Now().Unix(),
		"engagement_model": modelEngagement,
		"message_count":    len(messages),
		"engagement_raw":   engagementRaw,
		"input_prompt":     prompt,
		"eval_history":     evalHistory,
	}

	if err := emitEvent(deps.EventServiceURL, engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	if shouldEngage {
		// Race Condition Protection: Claim all events in the current context
		if deps.Redis != nil {
			for _, id := range contextEventIDs {
				deps.Redis.Set(ctx, "handled:event:"+id, "1", 2*time.Minute)
			}
			// Also claim the current triggering event
			deps.Redis.Set(ctx, "handled:event:"+input.EventID, "1", 2*time.Minute)
		}

		utils.ReportProcess(ctx, deps.Redis, deps.Discord, channelID, "Typing response...")
		deps.Discord.TriggerTyping(channelID)

		// VRAM Optimization
		_ = deps.Ollama.UnloadAllModelsExcept(ctx, responseModel)

		if deps.Redis != nil {
			deps.Redis.Expire(context.Background(), lockKey, 60*time.Second)
		}

		// --- 2. Load Profile for Personalization ---
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

		finalSystemPrompt := systemPrompt + dossierPrompt
		// Final construction of chat messages
		finalMessages := []ollama.Message{
			{Role: "system", Content: finalSystemPrompt},
		}
		finalMessages = append(finalMessages, messages...)
		finalMessages = append(finalMessages, ollama.Message{Role: "user", Content: content})

		streamMessageID, err := deps.Discord.InitStream(channelID, "<a:typing:1449387367315275786>")

		if err != nil {

			log.Printf("Failed to init stream: %v", err)

			return types.HandlerOutput{Success: false, Error: err.Error()},
				err

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
			return types.HandlerOutput{Success: false, Error: err.Error()},
				err
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
			"channel_id":         channelID,
			"channel_name":       input.EventData["channel_name"],
			"server_id":          input.EventData["server_id"],
			"server_name":        input.EventData["server_name"],
			"message_id":         finalMessageID,
			"content":            fullResponse,
			"timestamp":          time.Now().Format(time.RFC3339),
			"response_model":     responseModel,
			"response_raw":       fullResponse,
			"raw_input":          fmt.Sprintf("%v", finalMessages),
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

		// --- 3. Async Signal Extraction for User Profiling ---
		go func() {
			analysisModel := modelSummary
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
		},
		nil
}
