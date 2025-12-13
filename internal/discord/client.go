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
}

func NewClient(baseURL, eventSvcURL string) *Client {
	return &Client{
		BaseURL:     baseURL,
		EventSvcURL: eventSvcURL,
	}
}

func (c *Client) InitStream(channelID string) (string, error) {
	reqBody := map[string]string{
		"channel_id": channelID,
	}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := http.Post(c.BaseURL+"/message/stream/start", "application/json", bytes.NewBuffer(jsonData))
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

func (c *Client) CompleteStream(channelID, messageID, content string) {
	reqBody := map[string]string{
		"channel_id": channelID,
		"message_id": messageID,
		"content":    content,
	}
	jsonData, _ := json.Marshal(reqBody)
	_, _ = http.Post(c.BaseURL+"/message/stream/complete", "application/json", bytes.NewBuffer(jsonData))
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

func (c *Client) TriggerTyping(channelID string) {
	reqBody := map[string]string{"channel_id": channelID}
	jsonData, _ := json.Marshal(reqBody)

	resp, err := http.Post(c.BaseURL+"/typing", "application/json", bytes.NewBuffer(jsonData))
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

	resp, err := http.Post(c.BaseURL+"/status", "application/json", bytes.NewBuffer(jsonData))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
	}
}

// FetchContext calls the event service to get history.
// Note: The original code called Event Service, not Discord Service for context history.
func (c *Client) FetchContext(channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	// Use EventSvcURL here
	url := fmt.Sprintf("%s/events?channel=%s&max_length=25&order=desc&format=text&exclude_types=engagement.decision", c.EventSvcURL, channelID)

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

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
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

func (c *Client) PlayAudio(audioData []byte) error {

	req, err := http.NewRequest("POST", c.BaseURL+"/audio/play", bytes.NewBuffer(audioData))

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

func (c *Client) GetVoiceChannelUserCount(channelID string) (int, error) {

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/channel/voice/count?id=%s", c.BaseURL, channelID), nil)

	if err != nil {

		return 0, err

	}

	req.Header.Set("X-Service-Name", "dex-event-service")

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Do(req)

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
