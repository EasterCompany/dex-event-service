package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL     string
	EventSvcURL string // Needed for fetchContext legacy call, or maybe we move that logic?
	httpClient  *http.Client
}

func NewClient(baseURL, eventSvcURL string) *Client {
	return &Client{
		BaseURL:     baseURL,
		EventSvcURL: eventSvcURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) InitStream(channelID string, initialContent string) (string, error) {
	reqBody := map[string]string{
		"channel_id":      channelID,
		"initial_content": initialContent,
	}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.httpClient.Post(c.BaseURL+"/message/stream/start", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("init stream failed: %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["message_id"], nil
}

func (c *Client) UpdateStream(channelID, messageID, content string) {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
		"content":    content,
	}
	jsonData, _ := json.Marshal(reqBody)
	// Fire and forget
	go func() {
		_, _ = http.Post(c.BaseURL+"/message/stream/update", "application/json", bytes.NewBuffer(jsonData))
	}()
}

func (c *Client) CompleteStream(channelID, messageID, content string) (string, error) {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
		"content":    content,
	}
	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post(c.BaseURL+"/message/stream/complete", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return messageID, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return messageID, fmt.Errorf("complete stream failed: %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// If response is empty or invalid, just return original ID
		return messageID, nil
	}

	if finalID, ok := result["message_id"]; ok && finalID != "" {
		return finalID, nil
	}

	return messageID, nil
}

func (c *Client) PostMessage(targetID, content string) (string, error) {
	// Discord message limit is 2000 characters.
	// We'll split comfortably at 1900 to leave room for any overhead/formatting.
	const maxLen = 1900

	if len(content) <= maxLen {
		return c.postSingleMessage(targetID, content)
	}

	// Split content into chunks
	var lastMessageID string
	var chunks []string

	// Helper to split by lines first to preserve formatting
	lines := strings.Split(content, "\n")
	var currentChunk strings.Builder

	for _, line := range lines {
		if currentChunk.Len()+len(line)+1 > maxLen {
			// Current chunk is full, push it
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
		}
		if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n")
		}
		currentChunk.WriteString(line)
	}
	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	// If chunks is empty (e.g. one huge line), fallback to rune splitting
	if len(chunks) == 0 {
		runes := []rune(content)
		for i := 0; i < len(runes); i += maxLen {
			end := i + maxLen
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}
	}

	// Send chunks sequentially

	for i, chunk := range chunks {

		// Add pagination context if multiple chunks

		msgContent := chunk

		id, err := c.postSingleMessage(targetID, msgContent)

		if err != nil {
			return "", fmt.Errorf("failed to send chunk %d: %w", i+1, err)
		}
		lastMessageID = id
		// Slight delay to ensure order
		time.Sleep(200 * time.Millisecond)
	}

	return lastMessageID, nil
}

func (c *Client) PostMessageComplex(targetID, content string, embed *Embed) (string, error) {
	// Discord message limit is 2000 characters.
	const maxLen = 1900

	if len(content) <= maxLen {
		return c.postSingleMessageComplex(targetID, content, embed)
	}

	// Split content into chunks
	var lastMessageID string
	var chunks []string

	lines := strings.Split(content, "\n")
	var currentChunk strings.Builder

	for _, line := range lines {
		if currentChunk.Len()+len(line)+1 > maxLen {
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
		}
		if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n")
		}
		currentChunk.WriteString(line)
	}
	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	if len(chunks) == 0 {
		runes := []rune(content)
		for i := 0; i < len(runes); i += maxLen {
			end := i + maxLen
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}
	}

	// Send chunks
	for i, chunk := range chunks {
		var currentEmbed *Embed
		// Attach embed ONLY to the last chunk
		if i == len(chunks)-1 {
			currentEmbed = embed
		}

		id, err := c.postSingleMessageComplex(targetID, chunk, currentEmbed)
		if err != nil {
			return "", fmt.Errorf("failed to send chunk %d: %w", i+1, err)
		}
		lastMessageID = id
		time.Sleep(200 * time.Millisecond)
	}

	return lastMessageID, nil
}

func (c *Client) postSingleMessageComplex(targetID, content string, embed *Embed) (string, error) {
	type PostRequest struct {
		ChannelID string `json:"channel_id"`
		UserID    string `json:"user_id"`
		Content   string `json:"content"`
		Embed     *Embed `json:"embed"`
	}

	reqBody := PostRequest{
		Content: content,
		Embed:   embed,
	}

	if strings.HasPrefix(targetID, "channel:") {
		reqBody.ChannelID = strings.TrimPrefix(targetID, "channel:")
	} else {
		reqBody.UserID = targetID
	}

	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.httpClient.Post(c.BaseURL+"/post", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("post message complex failed: status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["message_id"], nil
}

func (c *Client) postSingleMessage(targetID, content string) (string, error) {
	reqBody := map[string]string{
		"content": content,
	}

	// Logic: If targetID starts with 'channel:', it's a channel.
	// If it's a pure numeric string (and long), it's likely a User ID for DMs.
	if strings.HasPrefix(targetID, "channel:") {
		reqBody["channel_id"] = strings.TrimPrefix(targetID, "channel:")
	} else {
		// Default to User ID for legacy compat and simplicity
		reqBody["user_id"] = targetID
	}

	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.httpClient.Post(c.BaseURL+"/post", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("post message failed: status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result["message_id"], nil
}

func (c *Client) DeleteMessage(channelID, messageID string) error {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodDelete, c.BaseURL+"/message/delete", bytes.NewBuffer(jsonData))
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

func (c *Client) AddReaction(channelID, messageID, emoji string) error {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
		"emoji":      emoji,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Post(c.BaseURL+"/message/react", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing reaction response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) TriggerTyping(channelID string) {
	reqBody := map[string]string{"channel_id": channelID}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.httpClient.Post(c.BaseURL+"/typing", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
}

func (c *Client) UpdateBotStatus(text string, status string, activityType int) {
	reqBody := map[string]interface{}{
		"status_text":   text,
		"online_status": status,
		"activity_type": activityType,
	}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := c.httpClient.Post(c.BaseURL+"/status", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
}

// FetchContext calls the event service to get history with a specific max length.
func (c *Client) FetchContext(channelID string, maxLength int) (string, error) {
	if channelID == "" {
		return "", nil
	}
	if maxLength <= 0 {
		maxLength = 25 // Default
	}
	// Use EventSvcURL here
	url := fmt.Sprintf("%s/events?channel=%s&max_length=%d&order=asc&format=text&exclude_types=engagement.decision", c.EventSvcURL, channelID, maxLength)

	resp, err := c.httpClient.Get(url)
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
	ID       string `json:"id"`
	Username string `json:"username"`
	Status   string `json:"status"`
	Activity string `json:"activity"`
}

type ChannelContextResponse struct {
	ChannelName string        `json:"channel_name"`
	GuildName   string        `json:"guild_name"`
	Users       []UserContext `json:"users"`
}

func (c *Client) FetchChannelMembers(channelID string) ([]UserContext, string, error) {
	if channelID == "" {
		return nil, "", nil
	}
	url := fmt.Sprintf("%s/context/channel?channel_id=%s", c.BaseURL, channelID)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var ctxResp ChannelContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&ctxResp); err != nil {
		return nil, "", err
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

	return ctxResp.Users, sb.String(), nil
}

func (c *Client) FetchMember(userID string) (*MemberContext, error) {
	url := fmt.Sprintf("%s/member/%s", c.BaseURL, userID)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var member MemberContext
	if err := json.NewDecoder(resp.Body).Decode(&member); err != nil {
		return nil, err
	}

	return &member, nil
}

type MemberContext struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	Level     string `json:"level"`
	Color     int    `json:"color"`
	Status    string `json:"status"`
}

func (c *Client) PlayAudio(audioData []byte) error {

	req, err := http.NewRequest("POST", c.BaseURL+"/audio/play", bytes.NewBuffer(audioData))

	if err != nil {

		return err

	}

	req.Header.Set("Content-Type", "audio/wav")

	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := c.httpClient.Do(req)

	if err != nil {

		return err

	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {

		return fmt.Errorf("discord service returned %d: %s", resp.StatusCode, resp.Status)

	}

	return nil

}

func (c *Client) GetVoiceChannelUserCount(channelID string) (int, error) {

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/channel/voice/count?id=%s", c.BaseURL, channelID), nil)

	if err != nil {

		return 0, err

	}

	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := c.httpClient.Do(req)

	if err != nil {

		return 0, err

	}

	defer func() {

		if err := resp.Body.Close(); err != nil {

			log.Printf("Error closing response body: %v", err)

		}

	}()

	if resp.StatusCode != http.StatusOK {

		return 0, fmt.Errorf("failed to get user count: %s", resp.Status)

	}

	var result struct {
		UserCount int `json:"user_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {

		return 0, err

	}

	return result.UserCount, nil

}

func (c *Client) PlayMusic(url string) error {
	reqBody := map[string]string{
		"url": url,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/audio/play_music", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Name", "dex-event-service")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord service returned %d: %s", resp.StatusCode, resp.Status)
	}

	return nil
}

func (c *Client) SetVoiceState(mute, deaf bool, reason string) {
	reqBody := map[string]interface{}{
		"mute":   mute,
		"deaf":   deaf,
		"reason": reason,
	}
	jsonData, _ := json.Marshal(reqBody)

	// Fire and forget
	go func() {
		resp, err := c.httpClient.Post(c.BaseURL+"/voice/state", "application/json", bytes.NewBuffer(jsonData))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
}
