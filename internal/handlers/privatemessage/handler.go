package privatemessage

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

	for _, foundURL := range foundURLs {
		if !strings.HasPrefix(foundURL, "https://discord.com/attachments/") {
			deps.Discord.UpdateBotStatus("Analyzing link...", "online", 3)
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
				summary, _ = deps.Ollama.Generate("dex-scraper-model", contentToSummarize, nil)
				summary = strings.TrimSpace(summary)
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
				attachments = append(attachments, map[string]interface{}{
					"id":           "virtual_" + foundURL,
					"url":          meta.ImageURL,
					"content_type": meta.ContentType,
					"filename":     fmt.Sprintf("link_expansion_%s.%s", meta.Provider, strings.Split(meta.ContentType, "/")[1]),
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

			if strings.HasPrefix(contentType, "image/") {
				deps.Discord.UpdateBotStatus("Analyzing image...", "online", 3)
				reportProcessStatus(deps, channelID, fmt.Sprintf("Analyzing Image: %s", filename), 0, startTime)

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
					prompt := "Describe this image concisely. If the image contains sexual content or nudity, output ONLY the tag <EXPLICIT_CONTENT_DETECTED/> and nothing else."
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
					"handler":         "private-message-handler", // Kept consistent with other handlers, or should be private? Original code said 'public-message-handler' in mod event but 'private-message-handler' in analysis? Original said public in mod, private in analysis. I'll stick to 'private-message-handler' here.
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

	lockKey := fmt.Sprintf("engagement:processing:%s", channelID)
	if deps.Redis != nil {
		locked, err := deps.Redis.SetNX(context.Background(), lockKey, "1", 60*time.Second).Result()
		if err == nil && !locked {
			log.Printf("Channel %s is already being processed by another handler. Exiting to allow aggregation.", channelID)
			return types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}, nil
		}
		defer deps.Redis.Del(context.Background(), lockKey)
	}

	contextHistory, err := deps.Discord.FetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
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

	shouldEngage := true
	engagementDecision := "TRUE"
	engagementRaw := "Forced engagement for Private Message"

	log.Printf("Engagement decision: %s (Forced)", engagementDecision)

	engagementEventData := map[string]interface{}{
		"type":             "engagement.decision",
		"decision":         engagementDecision,
		"reason":           "Private Message (Always Engage)",
		"handler":          "private-message-handler",
		"event_id":         input.EventID,
		"channel_id":       channelID,
		"user_id":          userID,
		"message_content":  content,
		"timestamp":        time.Now().Unix(),
		"engagement_model": "none",
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
		responseModel := "dex-private-message-model"

		streamMessageID, err = deps.Discord.InitStream(channelID)
		if err != nil {
			log.Printf("Failed to init stream: %v", err)
			return types.HandlerOutput{Success: false, Error: err.Error()}, err
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
			deps.Discord.CompleteStream(channelID, streamMessageID, "Error: I couldn't generate a response. Please try again later.")
			return types.HandlerOutput{Success: false, Error: err.Error()}, err
		}

		finalResponse := fullResponse
		if len(userMap) > 0 {
			finalResponse = utils.DenormalizeMentions(fullResponse, userMap)
		}
		deps.Discord.CompleteStream(channelID, streamMessageID, finalResponse)
		log.Printf("Generated response: %s", fullResponse)

		botEventData := map[string]interface{}{
			"type":           types.EventTypeMessagingBotSentMessage,
			"source":         "dex-event-service",
			"user_id":        "dexter",
			"user_name":      "Dexter",
			"channel_id":     channelID,
			"channel_name":   "DM",
			"server_id":      input.EventData["server_id"],
			"server_name":    "DM",
			"message_id":     streamMessageID,
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
	}, nil
}
