package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url" // Import for url.QueryEscape
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/EasterCompany/dex-event-service/types"
	"github.com/redis/go-redis/v9"
)

const OllamaURL = "http://127.0.0.1:11434"

var redisClient *redis.Client

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

func getRedisClient() (*redis.Client, error) {
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err != nil {
		return nil, err
	}

	var sm ServiceMap
	if err := json.Unmarshal(data, &sm); err != nil {
		return nil, err
	}

	// Basic lookup for local-cache-0 in 'os' category (common location)
	// The ServiceMap struct here is minimal, it might not capture 'os' key if it's map[string][]...
	// ServiceMap struct definition in this file is:
	// Services map[string][]struct { ... }
	// So it matches.

	for _, service := range sm.Services["os"] {
		if service.ID == "local-cache-0" {
			return redis.NewClient(&redis.Options{
				Addr: fmt.Sprintf("localhost:%s", service.Port), // Assuming localhost for handlers
			}), nil
		}
	}
	return nil, fmt.Errorf("redis service not found")
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

func getWebServiceURL() string {
	// Try to read service-map.json
	homeDir, _ := os.UserHomeDir()
	mapPath := filepath.Join(homeDir, "Dexter", "config", "service-map.json")

	data, err := os.ReadFile(mapPath)
	if err == nil {
		var sm ServiceMap
		if err := json.Unmarshal(data, &sm); err == nil {
			for _, service := range sm.Services["be"] { // dex-web-service is a Backend Service
				if service.ID == "dex-web-service" {
					return fmt.Sprintf("http://localhost:%s", service.Port)
				}
			}
		}
	}
	return "http://localhost:8201" // Fallback
}

type MetadataResponse struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Error       string `json:"error,omitempty"`
}

func fetchMetadata(linkURL string) (*MetadataResponse, error) {
	webServiceURL := getWebServiceURL()
	reqURL := fmt.Sprintf("%s/metadata?url=%s", webServiceURL, url.QueryEscape(linkURL))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("failed to call web service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("web service returned status %d: %s", resp.StatusCode, string(body))
	}

	var metaResp MetadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&metaResp); err != nil {
		return nil, fmt.Errorf("failed to decode web service response: %w", err)
	}

	return &metaResp, nil
}

func deleteMessage(channelID, messageID string) error {
	serviceURL := getDiscordServiceURL()
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodDelete, serviceURL+"/message/delete", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing delete response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
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
	url := fmt.Sprintf("%s/events?channel=%s&max_length=25&order=desc&format=text&exclude_types=engagement.decision", getEventServiceURL(), channelID)

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

type UserContext struct {
	Username string `json:"username"`
	Status   string `json:"status"`
	Activity string `json:"activity"`
}

type ChannelContextResponse struct {
	ChannelName string        `json:"channel_name"`
	GuildName   string        `json:"guild_name"`
	Users       []UserContext `json:"users"`
}

func fetchChannelMembers(channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	serviceURL := getDiscordServiceURL()
	url := fmt.Sprintf("%s/context/channel?channel_id=%s", serviceURL, channelID)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Service-Name", "dex-event-service")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var ctxResp ChannelContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&ctxResp); err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Channel Context (%s in %s):\n", ctxResp.ChannelName, ctxResp.GuildName))
	sb.WriteString("Active Users:\n")
	for _, u := range ctxResp.Users {
		statusLine := fmt.Sprintf("- %s: %s", u.Username, u.Status)
		if u.Activity != "" {
			statusLine += fmt.Sprintf(" (%s)", u.Activity)
		}
		sb.WriteString(statusLine + "\n")
	}

	return sb.String(), nil
}

func getLatestMessageID(channelID string) (string, error) {
	serviceURL := getDiscordServiceURL()
	url := fmt.Sprintf("%s/channel/latest?channel_id=%s", serviceURL, channelID)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Service-Name", "dex-event-service")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["last_message_id"], nil
}

func reportProcessStatus(channelID, state string, retryCount int, startTime time.Time) {
	if redisClient == nil {
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
	redisClient.Set(context.Background(), key, jsonBytes, 60*time.Second)
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

func triggerTyping(channelID string) {
	serviceURL := getDiscordServiceURL()
	reqBody := map[string]string{"channel_id": channelID}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := http.Post(serviceURL+"/typing", "application/json", bytes.NewBuffer(jsonData))
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
	// Initialize Redis
	var err error
	redisClient, err = getRedisClient()
	if err != nil {
		log.Printf("Warning: Failed to connect to Redis: %v. Visual analysis caching disabled.", err)
	} else {
		defer func() { _ = redisClient.Close() }()
	}

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
	mentionedBot, _ := input.EventData["mentioned_bot"].(bool)

	// Process attachments
	var attachments []map[string]interface{}
	if att, ok := input.EventData["attachments"].([]interface{}); ok {
		for _, a := range att {
			if m, ok := a.(map[string]interface{}); ok {
				attachments = append(attachments, m)
			}
		}
	}

	// Link Expansion: Extract URLs from content and fetch metadata
	urlRegex := `(https?:\/\/[^\s]+)`
	re := regexp.MustCompile(urlRegex)
	foundURLs := re.FindAllString(content, -1)

	for _, foundURL := range foundURLs {
		if !strings.HasPrefix(foundURL, "https://discord.com/attachments/") {
			log.Printf("Found external URL in message: %s", foundURL)
			meta, err := fetchMetadata(foundURL)
			if err != nil {
				log.Printf("Failed to fetch metadata for %s: %v", foundURL, err)
				continue
			}

			if meta.ImageURL != "" {
				log.Printf("Expanded URL %s to image: %s (Type: %s)", foundURL, meta.ImageURL, meta.ContentType)
				// Add as a "virtual" attachment for visual analysis
				attachments = append(attachments, map[string]interface{}{
					"id":           "virtual_" + foundURL, // Unique ID for caching
					"url":          meta.ImageURL,
					"content_type": meta.ContentType,
					"filename":     fmt.Sprintf("link_expansion_%s.%s", meta.Provider, strings.Split(meta.ContentType, "/")[1]),
					"size":         0, // Size unknown, vision model doesn't care
					"proxy_url":    "",
					"height":       0,
					"width":        0,
				})
			}
		}
	}

	log.Printf("public-message-handler processing for user %s in channel %s: %s (mentioned: %v, attachments: %d)", userID, channelID, content, mentionedBot, len(attachments))

	startTime := time.Now()
	reportProcessStatus(channelID, "Initializing", 0, startTime)
	defer func() {
		if redisClient != nil {
			redisClient.Del(context.Background(), fmt.Sprintf("process:info:%s", channelID))
		}
	}()

	updateBotStatus("Thinking...", "online", 3)
	defer updateBotStatus("Listening for events...", "online", 2)

	// --- Visual Analysis Phase ---
	visualContext := ""
	if len(attachments) > 0 {
		for _, att := range attachments {
			contentType, _ := att["content_type"].(string)
			url, _ := att["url"].(string)
			filename, _ := att["filename"].(string)
			id, _ := att["id"].(string) // Discord Attachment ID

			// Check if image
			if strings.HasPrefix(contentType, "image/") {
				updateBotStatus("Analyzing image...", "online", 3)
				reportProcessStatus(channelID, fmt.Sprintf("Analyzing Image: %s", filename), 0, startTime)

				// Check Cache
				var description string
				cacheKey := fmt.Sprintf("analysis:visual:%s", id)
				if redisClient != nil {
					if cached, err := redisClient.Get(context.Background(), cacheKey).Result(); err == nil {
						description = cached
						log.Printf("Using cached visual description for %s", filename)
					}
				}

				if description == "" {
					log.Printf("Downloading image: %s", filename)
					base64Img, err := downloadImageAsBase64(url)
					if err != nil {
						log.Printf("Failed to download image %s: %v", filename, err)
						continue
					}

					log.Printf("Generating visual description for %s...", filename)
					prompt := "Describe this image concisely. If the image contains sexual content or nudity, output ONLY the tag <EXPLICIT_CONTENT_DETECTED/> and nothing else."
					description, err = generateOllamaResponse("dex-vision-model", prompt, []string{base64Img})
					if err != nil {
						log.Printf("Vision model failed for %s: %v", filename, err)
						continue
					}

					// Check for explicit content tag
					if strings.Contains(description, "<EXPLICIT_CONTENT_DETECTED/>") {
						log.Printf("EXPLICIT CONTENT DETECTED in %s. Deleting message...", filename)

						// Delete the message
						messageID, _ := input.EventData["message_id"].(string)
						if messageID != "" {
							if err := deleteMessage(channelID, messageID); err != nil {
								log.Printf("Failed to delete explicit message: %v", err)
							} else {
								log.Printf("Successfully deleted explicit message %s", messageID)
							}
						}

						// Emit moderation event
						modEvent := map[string]interface{}{
							"type":         types.EventTypeModerationExplicitContentDeleted,
							"source":       "dex-event-service",
							"user_id":      userID,
							"user_name":    input.EventData["user_name"],
							"channel_id":   channelID,
							"channel_name": input.EventData["channel_name"],
							"server_id":    input.EventData["server_id"],
							"server_name":  input.EventData["server_name"],
							"timestamp":    time.Now().Format(time.RFC3339), // Use string format for validation
							"message_id":   messageID,
							"reason":       "Explicit content detected in attachment: " + filename,
							"handler":      "public-message-handler",
							"raw_output":   description,
						}
						if err := emitEvent(modEvent); err != nil {
							log.Printf("Failed to emit moderation event: %v", err)
						}

						// Stop processing immediately
						// Return success to ack the event processing
						output := types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}
						outputBytes, _ := json.Marshal(output)
						fmt.Println(string(outputBytes))
						return
					}

					// Cache the result
					if redisClient != nil {
						redisClient.Set(context.Background(), cacheKey, description, 24*time.Hour)
					}
				}

				log.Printf("Visual description for %s: %s", filename, description)
				visualContext += fmt.Sprintf("\n[Attachment: %s (Image) - Description: %s]", filename, description)

				// Emit child event for analysis
				analysisEvent := map[string]interface{}{
					"type":            "analysis.visual.completed",
					"parent_event_id": input.EventID,
					"handler":         "public-message-handler",
					"filename":        filename,
					"description":     description,
					"timestamp":       time.Now().Unix(),
				}
				_ = emitEvent(analysisEvent)
			}
		}
	}

	// Append visual context to content for engagement/response models
	if visualContext != "" {
		content += visualContext
	}

	// --- Aggregating Lock Check ---
	// Check if another handler is already processing this channel.
	// If so, we exit immediately. The active handler will pick up our message via the aggregation loop.
	lockKey := fmt.Sprintf("engagement:processing:%s", channelID)
	if redisClient != nil {
		// Use SetNX to acquire lock. If false, someone else has it.
		// We set a 60s TTL to prevent deadlocks if the handler crashes.
		// Note: If this is the VERY SAME handler restarting? No, handlers are new processes.
		locked, err := redisClient.SetNX(context.Background(), lockKey, "1", 60*time.Second).Result()
		if err == nil && !locked {
			log.Printf("Channel %s is already being processed by another handler. Exiting to allow aggregation.", channelID)
			// We return success effectively "absorbing" this event into the running process
			output := types.HandlerOutput{Success: true, Events: []types.HandlerOutputEvent{}}
			outputBytes, _ := json.Marshal(output)
			fmt.Println(string(outputBytes))
			return
		}
		// Ensure we release the lock when we are done (or if we crash/exit early)
		defer redisClient.Del(context.Background(), lockKey)
	}

	shouldEngage := false
	engagementReason := "Evaluated by dex-engagement-model"
	var engagementRaw string

	// Fetch context
	contextHistory, err := fetchContext(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch context: %v", err)
	}

	// Fetch channel members (active users)
	channelMembers, err := fetchChannelMembers(channelID)
	if err != nil {
		log.Printf("Warning: Failed to fetch channel members: %v", err)
	} else if channelMembers != "" {
		contextHistory += "\n\n" + channelMembers
	}

	if mentionedBot {
		log.Printf("Bot was mentioned, forcing engagement.")
		shouldEngage = true
		engagementReason = "Direct mention"
	} else {
		// 1. Check Engagement
		reportProcessStatus(channelID, "Checking Engagement", 0, startTime)
		prompt := fmt.Sprintf("Context:\n%s\n\nCurrent Message:\n%s", contextHistory, content)
		var err error
		engagementRaw, err = generateOllamaResponse("dex-engagement-model", prompt, nil)
		if err != nil {
			log.Printf("Engagement check failed: %v", err)
			// Fail gracefully, maybe don't engage if model fails
		} else {
			engagementDecision := strings.ToUpper(strings.TrimSpace(engagementRaw))
			shouldEngage = strings.Contains(engagementDecision, "TRUE")
			log.Printf("Engagement decision: %s (%v)", engagementDecision, shouldEngage)
		}
	}

	// Construct a child event for engagement decision
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

	// Emit decision immediately
	if err := emitEvent(engagementEventData); err != nil {
		log.Printf("Failed to emit engagement decision event: %v", err)
	}

	// 2. Engage if needed (Aggregating Loop)
	if shouldEngage {
		maxRetries := 3 // Prevent infinite loops
		retryCount := 0

		for {
			updateBotStatus("Typing response...", "online", 0)
			triggerTyping(channelID)

			statusMsg := "Generating Response"
			if retryCount > 0 {
				statusMsg = fmt.Sprintf("Regenerating (Interruption %d)", retryCount)
			}
			reportProcessStatus(channelID, statusMsg, retryCount, startTime)

			// Refresh the lock TTL to keep other handlers away while we work
			if redisClient != nil {
				redisClient.Expire(context.Background(), lockKey, 60*time.Second)
			}

			// If this is a retry, we need to refresh context to capture the interruption
			if retryCount > 0 {
				log.Printf("Refreshing context for aggregation (Attempt %d)...", retryCount)
				newContext, err := fetchContext(channelID)
				if err == nil {
					contextHistory = newContext
					if channelMembers != "" {
						contextHistory += "\n\n" + channelMembers
					}
				}
			}

			prompt := fmt.Sprintf("Context:\n%s\n\nUser: %s", contextHistory, content)

			// If retrying, we might just prompt with context history as 'content' is old?
			// Actually, 'contextHistory' from `fetchContext` includes the latest messages.
			// 'content' variable holds the *original* trigger message.
			// If we are retrying, the context is what matters most.
			// We should probably rely on Context mostly.

			// Capture the state of the channel BEFORE we start thinking hard.
			snapshotMessageID, _ := getLatestMessageID(channelID)

			var err error
			responseModel := "dex-public-message-model"
			response, err := generateOllamaResponse(responseModel, prompt, nil)

			if err != nil {
				log.Printf("Response generation failed: %v", err)
				break // Exit loop on error
			}

			log.Printf("Generated response: %s", response)

			// Check for interruption (Has the world changed while we were thinking?)
			currentLatestID, err := getLatestMessageID(channelID)
			if err == nil && snapshotMessageID != "" && currentLatestID != "" && snapshotMessageID != currentLatestID {
				if retryCount < maxRetries {
					log.Printf("INTERRUPTION DETECTED: Snapshot ID %s != Current ID %s. Re-aggregating...", snapshotMessageID, currentLatestID)
					retryCount++
					continue // Loop again!
				} else {
					log.Printf("Max retries reached. Sending despite interruption.")
				}
			}

			// If we are here, either no interruption or max retries.
			metadata := map[string]interface{}{
				"response_model": responseModel,
				"response_raw":   response,
				"raw_input":      prompt,
			}

			if err := postToDiscord(channelID, response, metadata); err != nil {
				log.Printf("Failed to post to discord: %v", err)
			}
			break // Done!
		}
	}

	// Construct HandlerOutput
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
