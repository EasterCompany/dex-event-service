package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const DefaultURL = "http://127.0.0.1:11434"

type Client struct {
	BaseURL string
}

func NewClient(url string) *Client {
	if url == "" {
		url = DefaultURL
	}
	return &Client{BaseURL: url}
}

type GenerateRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Images  []string               `json:"images,omitempty"`
	Stream  bool                   `json:"stream"`
	Options map[string]interface{} `json:"options,omitempty"`
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
	Model    string                 `json:"model"`
	Messages []Message              `json:"messages"`
	Stream   bool                   `json:"stream"`
	Format   string                 `json:"format,omitempty"` // json or empty
	Options  map[string]interface{} `json:"options,omitempty"`
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

func (c *Client) Chat(ctx context.Context, model string, messages []Message) (Message, error) {
	reqBody := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/chat", bytes.NewBuffer(jsonData))
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
			log.Printf("Error closing ollama chat response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Message{}, fmt.Errorf("ollama chat returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var response ChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return Message{}, err
	}
	return response.Message, nil
}

func (c *Client) ChatStream(ctx context.Context, model string, messages []Message, options map[string]interface{}, callback func(string)) (GenerationStats, error) {
	reqBody := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
		Options:  options,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return GenerationStats{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/chat", bytes.NewBuffer(jsonData))
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
			log.Printf("Error closing ollama chat response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return GenerationStats{}, fmt.Errorf("ollama chat returned status %d: %s", resp.StatusCode, string(body))
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

		var chunk ChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
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
	return stats, nil
}

func (c *Client) Generate(model, prompt string, images []string) (string, GenerationStats, error) {
	return c.GenerateWithContext(context.Background(), model, prompt, images, nil)
}

func (c *Client) GenerateWithContext(ctx context.Context, model, prompt string, images []string, options map[string]interface{}) (string, GenerationStats, error) {
	reqBody := GenerateRequest{
		Model:   model,
		Prompt:  prompt,
		Images:  images,
		Stream:  false,
		Options: options,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", GenerationStats{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/generate", bytes.NewBuffer(jsonData))
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
			log.Printf("Error closing ollama response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", GenerationStats{}, fmt.Errorf("ollama returned status: %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var response GenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
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
	reqBody := GenerateRequest{
		Model:   model,
		Prompt:  prompt,
		Images:  images,
		Stream:  true,
		Options: options,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return GenerationStats{}, err
	}

	resp, err := http.Post(c.BaseURL+"/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return GenerationStats{}, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing ollama response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return GenerationStats{}, fmt.Errorf("ollama returned status: %d", resp.StatusCode)
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
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
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
	return stats, nil
}
