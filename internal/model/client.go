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
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	EvalCount          int    `json:"eval_count,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
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
	c.emit("system.cognitive.model_load", map[string]interface{}{
		"model":  model,
		"method": "chat",
	})

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

	// The dedicated service returns the raw Ollama/Llama response stream or body.
	// For Chat (non-stream), it returns the Ollama ChatResponse JSON.
	body, _ := io.ReadAll(resp.Body)
	var response ChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		// Fallback: It might be a simple JSON from a custom service
		var simpleResp map[string]string
		if err2 := json.Unmarshal(body, &simpleResp); err2 == nil {
			if val, ok := simpleResp["response"]; ok {
				return Message{Role: "assistant", Content: val}, nil
			}
		}
		return Message{}, fmt.Errorf("failed to unmarshal hub response: %v. Body: %s", err, string(body))
	}
	return response.Message, nil
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
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return stats, err
		}

		// Try to parse as Ollama ChatResponse
		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err == nil {
			if chunk.Message.Content != "" {
				callback(chunk.Message.Content)
			}
			if chunk.Done {
				stats.EvalCount = chunk.EvalCount
				stats.PromptEvalCount = chunk.PromptEvalCount
				stats.TotalDuration = time.Duration(chunk.TotalDuration)
				stats.LoadDuration = time.Duration(chunk.LoadDuration)
				stats.PromptEvalDuration = time.Duration(chunk.PromptEvalDuration)
				stats.EvalDuration = time.Duration(chunk.EvalDuration)
				break
			}
		}
	}
	return stats, nil
}

func (c *Client) Generate(model, prompt string, images []string) (string, GenerationStats, error) {
	return c.GenerateWithContext(context.Background(), model, prompt, images, map[string]interface{}{
		"num_thread": runtime.NumCPU(),
	})
}

func (c *Client) GenerateWithContext(ctx context.Context, model, prompt string, images []string, options map[string]interface{}) (string, GenerationStats, error) {
	c.emit("system.cognitive.model_load", map[string]interface{}{
		"model":  model,
		"method": "generate",
	})

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
				return val, GenerationStats{}, nil
			}
		}
		return "", GenerationStats{}, err
	}

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
				break
			}
		}
	}
	return stats, nil
}

type ProcessModel struct {
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	Size      int64     `json:"size"`
	Digest    string    `json:"digest"`
	Details   Details   `json:"details"`
	ExpiresAt time.Time `json:"expires_at"`
	SizeVRAM  int64     `json:"size_vram"`
}

type Details struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type ProcessResponse struct {
	Models []ProcessModel `json:"models"`
}

// ListRunningModels returns a list of currently loaded models via /api/ps
func (c *Client) ListRunningModels(ctx context.Context) ([]ProcessModel, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub ps returned status: %d", resp.StatusCode)
	}

	var response ProcessResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Models, nil
}

// UnloadModel forces a model to unload by sending a request with keep_alive: 0
func (c *Client) UnloadModel(ctx context.Context, model string, reason string) error {
	if reason == "" {
		reason = "manual"
	}

	c.emit("system.cognitive.model_unload", map[string]interface{}{
		"model":  model,
		"reason": reason,
	})

	payload := map[string]interface{}{
		"model":      model,
		"keep_alive": 0,
	}

	jsonData, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/generate", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// UnloadAllModelsExcept unloads all running models except the specified one.
func (c *Client) UnloadAllModelsExcept(ctx context.Context, keepModel string) error {
	models, err := c.ListRunningModels(ctx)
	if err != nil {
		return err
	}

	// Check if we are in Voice Mode
	isVoiceMode := false
	if c.Redis != nil {
		val, _ := c.Redis.Get(ctx, "system:cognitive_lock").Result()
		if val == "Voice Mode" {
			isVoiceMode = true
		}
	}

	for _, m := range models {
		if m.Name != keepModel && m.Model != keepModel {
			// PROTECTION: Never unload voice-critical models during Voice Mode
			if isVoiceMode {
				if m.Name == "dex-transcription" || m.Name == "dex-engagement-model" ||
					m.Model == "dex-transcription" || m.Model == "dex-engagement-model" {
					log.Printf("VRAM Optimization: Preserving protected voice model %s", m.Name)
					continue
				}
			}

			// ONLY unload if it's using VRAM.
			// Models on CPU (SizeVRAM == 0) don't cause thrashing and should be preserved for speed.
			if m.SizeVRAM > 0 {
				log.Printf("Optimizing VRAM: Unloading idle model %s (%d bytes VRAM)...", m.Name, m.SizeVRAM)
				if err := c.UnloadModel(ctx, m.Name, "VRAM Optimization"); err != nil {
					log.Printf("Failed to unload %s: %v", m.Name, err)
				}
			} else {
				log.Printf("VRAM Optimization: Preserving CPU-resident model %s", m.Name)
			}
		}
	}
	return nil
}
