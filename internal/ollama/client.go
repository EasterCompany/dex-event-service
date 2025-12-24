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
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"`
	Stream bool     `json:"stream"`
}

type GenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func (c *Client) Generate(model, prompt string, images []string) (string, error) {
	return c.GenerateWithContext(context.Background(), model, prompt, images)
}

func (c *Client) GenerateWithContext(ctx context.Context, model, prompt string, images []string) (string, error) {
	reqBody := GenerateRequest{
		Model:  model,
		Prompt: prompt,
		Images: images,
		Stream: false,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/generate", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
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
	var response GenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", err
	}
	return response.Response, nil
}

func (c *Client) GenerateStream(model, prompt string, images []string, callback func(string)) error {
	reqBody := GenerateRequest{
		Model:  model,
		Prompt: prompt,
		Images: images,
		Stream: true,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	resp, err := http.Post(c.BaseURL+"/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Error closing ollama response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned status: %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		var chunk GenerateResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Response != "" {
			callback(chunk.Response)
		}
		if chunk.Done {
			break
		}
	}
	return nil
}
