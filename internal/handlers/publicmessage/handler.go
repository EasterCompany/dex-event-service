package publicmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/internal/handlers"
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

func reportProcessStatus(deps *handlers.Dependencies, channelID, state string, retryCount int, startTime time.Time) {
	if deps.Redis == nil {
		return
	}

	key := fmt.Sprintf("process:info:%s", channelID)
	data := map[string]interface{}{
		"channel_id": channelID,
		"state":      state,
		"retries":    retryCount,
		"start_time": startTime.Unix(),
		"pid":        os.Getpid(),
		"updated_at": time.Now().Unix(),
	}

	jsonBytes, _ := json.Marshal(data)
	deps.Redis.Set(context.Background(), key, jsonBytes, 60*time.Second)
}

func Handle(ctx context.Context, input types.HandlerInput, deps *handlers.Dependencies) (types.HandlerOutput, error) {
	content, _ := input.EventData["content"].(string)
	channelID, _ := input.EventData["channel_id"].(string)
	userID, _ := input.EventData["user_id"].(string)
	mentionedBot, _ := input.EventData["mentioned_bot"].(bool)

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
			routerOutput, err := deps.Ollama.Generate("dex-router-model", routerInput, nil)
			if err != nil {
				log.Printf("dex-router-model failed: %v, defaulting to STATIC", err)
			} else {
				decision = strings.ToUpper(strings.TrimSpace(routerOutput))
				log.Printf("dex-router-model decision for %s: %s", foundURL, decision)
			}
			// --- End Router Model Logic ---

			var meta *web.MetadataResponse
			var webView *web.WebViewResponse
			var fetchErr error

			if decision == "VISUAL" {
				deps.Discord.UpdateBotStatus("Viewing link...", "online", 3)
				webView, fetchErr = deps.Web.FetchWebView(foundURL)
				if fetchErr != nil {
					log.Printf("Failed to fetch web view for %s: %v, falling back to STATIC", foundURL, fetchErr)
					// Fallback to static if webview fails
					deps.Discord.UpdateBotStatus("Analyzing link...", "online", 3)
					meta, fetchErr = deps.Web.FetchMetadata(foundURL)
				}
			} else {
				deps.Discord.UpdateBotStatus("Analyzing link...", "online", 3)
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
					currentSummary, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
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
					currentSummary, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
					currentSummary = strings.TrimSpace(currentSummary)
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

	// Music Logic: Check for YouTube links in music channel or if bot is mentioned
	if channelID == "1437617331529580614" || mentionedBot {
		for _, foundURL := range foundURLs {
			if strings.Contains(foundURL, "youtube.com") || strings.Contains(foundURL, "youtu.be") {
				log.Printf("Music request detected: %s", foundURL)
				deps.Discord.UpdateBotStatus("Playing music...", "online", 0)
				if err := deps.Discord.PlayMusic(foundURL); err != nil {
					log.Printf("Failed to play music: %v", err)
				}
				// If it was the music channel, we stop here (no engagement needed)
				if channelID == "1437617331529580614" {
					return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}},
						nil
				}
				break // Only play first link
			}
		}
	}

	log.Printf("public-message-handler processing for user %s in channel %s: %s (mentioned: %v, attachments: %d)", userID, channelID, content, mentionedBot, len(attachments))

	startTime := time.Now()
	reportProcessStatus(deps, channelID, "Initializing", 0, startTime)
	defer func() {
		if deps.Redis != nil {
			deps.Redis.Del(context.Background(), fmt.Sprintf("process:info:%s", channelID))
		}
	}()

	deps.Discord.UpdateBotStatus("Thinking...", "online", 3)
	defer deps.Discord.UpdateBotStatus("Listening for events...", "online", 2)

	visualContext := ""
	if len(attachments) > 0 {
		for _, att := range attachments {
			contentType, _ := att["content_type"].(string)
			url, _ := att["url"].(string)
			filename, _ := att["filename"].(string)
			id, _ := att["id"].(string)
			base64Data, hasBase64 := att["base64"].(string) // New: Check for base64 data

			if strings.HasPrefix(contentType, "image/") {
				deps.Discord.UpdateBotStatus("Analyzing image...", "online", 3)
				reportProcessStatus(deps, channelID, fmt.Sprintf("Analyzing Image: %s", filename), 0, startTime)

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
					prompt := "Describe this image concisely. If the image contains sexual content or nudity, output ONLY the tag <EXPLICIT_CONTENT_DETECTED/> and nothing else."
					var err error
					description, err = deps.Ollama.Generate("dex-vision-model", prompt, []string{base64Img})
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

	shouldEngage := false
	engagementReason := "Evaluated by dex-engagement-model"
	var engagementRaw string

	contextHistory, err := deps.Discord.FetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
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

	if channelMembers != "" {
		contextHistory += "\n\n" + channelMembers
	}

	// Restricted channels (Analysis/Moderation only, no unsolicited engagement)
	restrictedChannels := map[string]bool{
		"1437617331529580614": true, // Music
		"1381915374181810236": true, // Memes
	}

	if mentionedBot {
		log.Printf("Bot was mentioned, forcing engagement.")
		shouldEngage = true
		engagementReason = "Direct mention"
	} else if restrictedChannels[channelID] {
		log.Printf("Channel %s is restricted (analysis only). Skipping engagement check.", channelID)
		shouldEngage = false
		engagementReason = "Restricted Channel (Analysis Only)"
	} else {
		reportProcessStatus(deps, channelID, "Checking Engagement", 0, startTime)
		prompt := fmt.Sprintf("Context:\n%s\n\nCurrent Message:\n%s", contextHistory, content)
		var err error
		engagementRaw, err = deps.Ollama.Generate("dex-engagement-model", prompt, nil)
		if err != nil {
			log.Printf("Engagement check failed: %v", err)
		} else {
			engagementDecision := strings.ToUpper(strings.TrimSpace(engagementRaw))
			shouldEngage = strings.Contains(engagementDecision, "TRUE")
			log.Printf("Engagement decision: %s (%v)", engagementDecision, shouldEngage)
		}
	}

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

	if err := emitEvent(deps.EventServiceURL, engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	if shouldEngage {
		retryCount := 0
		var streamMessageID string

		deps.Discord.UpdateBotStatus("Typing response...", "online", 0)
		deps.Discord.TriggerTyping(channelID)
		reportProcessStatus(deps, channelID, "Generating Response", retryCount, startTime)

		if deps.Redis != nil {
			deps.Redis.Expire(context.Background(), lockKey, 60*time.Second)
		}
		prompt := fmt.Sprintf("Context:\n%s\n\nUser: %s", contextHistory, content)
		responseModel := "dex-public-message-model"

		streamMessageID, err = deps.Discord.InitStream(channelID)
		if err != nil {
			log.Printf("Failed to init stream: %v", err)
			return types.HandlerOutput{Success: false, Error: err.Error()},
				err
		}

		fullResponse := ""
		err = deps.Ollama.GenerateStream(responseModel, prompt, nil, func(chunk string) {
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

		botEventData := map[string]interface{}{
			"type":           types.EventTypeMessagingBotSentMessage,
			"source":         "dex-event-service",
			"user_id":        "dexter",
			"user_name":      "Dexter",
			"channel_id":     channelID,
			"channel_name":   input.EventData["channel_name"],
			"server_id":      input.EventData["server_id"],
			"server_name":    input.EventData["server_name"],
			"message_id":     finalMessageID,
			"content":        fullResponse,
			"timestamp":      time.Now().Format(time.RFC3339),
			"response_model": responseModel,
			"response_raw":   fullResponse,
			"raw_input":      prompt,
		}
		if err := emitEvent(deps.EventServiceURL, botEventData); err != nil {
			log.Printf("Warning: Failed to emit event: %v", err)
		}
	}

	return types.HandlerOutput{
			Success: true,
			Events:  []types.HandlerOutputEvent{},
		},
		nil
}
