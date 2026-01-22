package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/redis/go-redis/v9"
)

const DefaultHubURL = "http://127.0.0.1:8400"

type Client struct {
	BaseURL       string
	Redis         *redis.Client
	EventCallback func(eventType string, data map[string]interface{})
}

func NewClient(url string, rdb *redis.Client) *Client {
	if url == "" {
		url = DefaultHubURL
	}
	return &Client{BaseURL: url, Redis: rdb}
}

// ModelRequest represents the standard request format for the Model Hub
type ModelRequest struct {
	Model    string          `json:"model"`
	Prompt   string          `json:"prompt,omitempty"`
	Messages []Message       `json:"messages,omitempty"`
	Stream   bool            `json:"stream"`
	Options  json.RawMessage `json:"options,omitempty"` // Pass through options opaque
}

func (c *Client) SetEventCallback(callback func(eventType string, data map[string]interface{})) {
	c.EventCallback = callback
}

type GenerateRequest struct {
	Model     string                 `json:"model"`
	Prompt    string                 `json:"prompt"`
	Images    []string               `json:"images,omitempty"`
	Stream    bool                   `json:"stream"`
	Options   map[string]interface{} `json:"options,omitempty"`
	KeepAlive interface{}            `json:"keep_alive,omitempty"`
}

type GenerateResponse struct {
	Response           string  `json:"response"`
	Message            Message `json:"message"` // Fallback
	Done               bool    `json:"done"`
	EvalCount          int     `json:"eval_count,omitempty"`
	PromptEvalCount    int     `json:"prompt_eval_count,omitempty"`
	TotalDuration      int64   `json:"total_duration,omitempty"`
	LoadDuration       int64   `json:"load_duration,omitempty"`
	PromptEvalDuration int64   `json:"prompt_eval_duration,omitempty"`
	EvalDuration       int64   `json:"eval_duration,omitempty"`
}

type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Name    string   `json:"name,omitempty"`
	Images  []string `json:"images,omitempty"`
}

type ChatRequest struct {
	Model     string                 `json:"model"`
	Messages  []Message              `json:"messages"`
	Stream    bool                   `json:"stream"`
	Format    string                 `json:"format,omitempty"` // json or empty
	Options   map[string]interface{} `json:"options,omitempty"`
	KeepAlive interface{}            `json:"keep_alive,omitempty"`
}

type ChatResponse struct {
	Model              string  `json:"model"`
	Message            Message `json:"message"`
	Response           string  `json:"response"` // Dedicated spoke fallback
	Done               bool    `json:"done"`
	EvalCount          int     `json:"eval_count,omitempty"`
	PromptEvalCount    int     `json:"prompt_eval_count,omitempty"`
	TotalDuration      int64   `json:"total_duration,omitempty"`
	LoadDuration       int64   `json:"load_duration,omitempty"`
	PromptEvalDuration int64   `json:"prompt_eval_duration,omitempty"`
	EvalDuration       int64   `json:"eval_duration,omitempty"`
}

type GenerationStats struct {
	EvalCount          int
	PromptEvalCount    int
	TotalDuration      time.Duration
	LoadDuration       time.Duration
	PromptEvalDuration time.Duration
	EvalDuration       time.Duration
}

func (c *Client) emit(eventType string, data map[string]interface{}) {
	if c.EventCallback != nil {
		c.EventCallback(eventType, data)
	}
}

func (c *Client) Chat(ctx context.Context, model string, messages []Message) (Message, error) {
	return c.ChatWithOptions(ctx, model, messages, map[string]interface{}{
		"num_thread": runtime.NumCPU(),
	})
}

func (c *Client) ChatWithOptions(ctx context.Context, model string, messages []Message, options map[string]interface{}) (Message, error) {
	// Convert options to RawMessage for pass-through
	optsBytes, _ := json.Marshal(options)

	reqBody := ModelRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
		Options:  optsBytes,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Message{}, err
	}

	// HIT THE HUB
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/model/run", bytes.NewBuffer(jsonData))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing hub response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Message{}, fmt.Errorf("hub returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var response ChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		// Fallback: It might be a simple JSON from a custom service
		var simpleResp map[string]string
		if err2 := json.Unmarshal(body, &simpleResp); err2 == nil {
			if val, ok := simpleResp["response"]; ok {
				c.emit("system.cognitive.model_inference", map[string]interface{}{
					"model":  model,
					"method": "chat",
				})
				return Message{Role: "assistant", Content: val}, nil
			}
		}
		return Message{}, fmt.Errorf("failed to unmarshal hub response: %v. Body: %s", err, string(body))
	}

	content := response.Message.Content
	if content == "" {
		content = response.Response
	}

	// Safety: If still empty, log warning
	if content == "" {
		log.Printf("Warning: Model %s returned empty content in ChatWithOptions", model)
	}

	c.emit("system.cognitive.model_inference", map[string]interface{}{
		"model":             model,
		"method":            "chat",
		"eval_count":        response.EvalCount,
		"prompt_eval_count": response.PromptEvalCount,
		"duration_ms":       response.TotalDuration / 1000000, // ns to ms
	})

	return Message{Role: "assistant", Content: content}, nil
}

func (c *Client) ChatStream(ctx context.Context, model string, messages []Message, options map[string]interface{}, callback func(string)) (GenerationStats, error) {
	// Convert options to RawMessage for pass-through
	optsBytes, _ := json.Marshal(options)

	reqBody := ModelRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
		Options:  optsBytes,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return GenerationStats{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/model/run", bytes.NewBuffer(jsonData))
	if err != nil {
		return GenerationStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return GenerationStats{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing hub response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return GenerationStats{}, fmt.Errorf("hub returned status %d: %s", resp.StatusCode, string(body))
	}

	stats := GenerationStats{}
	reader := bufio.NewReader(resp.Body)
	receivedAnyContent := false

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
		}

		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// Try to parse as Ollama ChatResponse
		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err == nil {
			content := chunk.Message.Content
			if content == "" {
				content = chunk.Response
			}

			if content != "" {
				callback(content)
				receivedAnyContent = true
			}

			if chunk.Done {
				stats.EvalCount = chunk.EvalCount
				stats.PromptEvalCount = chunk.PromptEvalCount
				stats.TotalDuration = time.Duration(chunk.TotalDuration)
				stats.LoadDuration = time.Duration(chunk.LoadDuration)
				stats.PromptEvalDuration = time.Duration(chunk.PromptEvalDuration)
				stats.EvalDuration = time.Duration(chunk.EvalDuration)

				c.emit("system.cognitive.model_inference", map[string]interface{}{
					"model":             model,
					"method":            "chat-stream",
					"eval_count":        chunk.EvalCount,
					"prompt_eval_count": chunk.PromptEvalCount,
					"duration_ms":       chunk.TotalDuration / 1000000,
				})
				break
			}
		} else {
			log.Printf("ChatStream: Failed to unmarshal chunk from %s: %v. Raw: %s", model, err, string(line))
		}
	}

	if !receivedAnyContent {
		log.Printf("Warning: Model %s returned NO content chunks in ChatStream", model)
	}

	return stats, nil
}

func (c *Client) Generate(model, prompt string, images []string) (string, GenerationStats, error) {
	return c.GenerateWithContext(context.Background(), model, prompt, images, map[string]interface{}{
		"num_thread": runtime.NumCPU(),
	})
}

func (c *Client) GenerateWithContext(ctx context.Context, model, prompt string, images []string, options map[string]interface{}) (string, GenerationStats, error) {
	// Convert options
	optsBytes, _ := json.Marshal(options)

	reqBody := ModelRequest{
		Model:   model,
		Prompt:  prompt,
		Stream:  false,
		Options: optsBytes,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", GenerationStats{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/model/run", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", GenerationStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", GenerationStats{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing hub response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", GenerationStats{}, fmt.Errorf("hub returned status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var response GenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		// Fallback for simple map response
		var simpleResp map[string]string
		if err2 := json.Unmarshal(body, &simpleResp); err2 == nil {
			if val, ok := simpleResp["response"]; ok {
				c.emit("system.cognitive.model_inference", map[string]interface{}{
					"model":  model,
					"method": "generate",
				})
				return val, GenerationStats{}, nil
			}
		}
		return "", GenerationStats{}, err
	}

	c.emit("system.cognitive.model_inference", map[string]interface{}{
		"model":             model,
		"method":            "generate",
		"eval_count":        response.EvalCount,
		"prompt_eval_count": response.PromptEvalCount,
		"duration_ms":       response.TotalDuration / 1000000, // ns to ms
	})

	stats := GenerationStats{
		EvalCount:          response.EvalCount,
		PromptEvalCount:    response.PromptEvalCount,
		TotalDuration:      time.Duration(response.TotalDuration),
		LoadDuration:       time.Duration(response.LoadDuration),
		PromptEvalDuration: time.Duration(response.PromptEvalDuration),
		EvalDuration:       time.Duration(response.EvalDuration),
	}

	return response.Response, stats, nil
}

func (c *Client) GenerateStream(model, prompt string, images []string, options map[string]interface{}, callback func(string)) (GenerationStats, error) {
	// Convert options
	optsBytes, _ := json.Marshal(options)

	reqBody := ModelRequest{
		Model:   model,
		Prompt:  prompt,
		Stream:  true,
		Options: optsBytes,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return GenerationStats{}, err
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/model/run", bytes.NewBuffer(jsonData))
	if err != nil {
		return GenerationStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return GenerationStats{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing hub response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return GenerationStats{}, fmt.Errorf("hub returned status: %d", resp.StatusCode)
	}

	stats := GenerationStats{}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
		}

		var chunk GenerateResponse
		if err := json.Unmarshal(line, &chunk); err == nil {
			if chunk.Response != "" {
				callback(chunk.Response)
			}
			if chunk.Done {
				stats.EvalCount = chunk.EvalCount
				stats.PromptEvalCount = chunk.PromptEvalCount
				stats.TotalDuration = time.Duration(chunk.TotalDuration)
				stats.LoadDuration = time.Duration(chunk.LoadDuration)
				stats.PromptEvalDuration = time.Duration(chunk.PromptEvalDuration)
				stats.EvalDuration = time.Duration(chunk.EvalDuration)

				c.emit("system.cognitive.model_inference", map[string]interface{}{
					"model":             model,
					"method":            "generate-stream",
					"eval_count":        chunk.EvalCount,
					"prompt_eval_count": chunk.PromptEvalCount,
					"duration_ms":       chunk.TotalDuration / 1000000,
				})
				break
			}
		}
	}
	return stats, nil
}
