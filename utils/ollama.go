package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultOllamaURL = "http://127.0.0.1:11434"

// ModelInfo reflects a single model entry returned by the /api/tags endpoint.
type ModelInfo struct {
	Name       string    `json:"name"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
	Details    struct {
		Format            string   `json:"format"`
		Family            string   `json:"family"`
		Families          []string `json:"families"`
		ParameterSize     string   `json:"parameter_size"`
		QuantizationLevel string   `json:"quantization_level"`
	} `json:"details"`
}

// ListModelsResponse handles the full JSON response from /api/tags.
type ListModelsResponse struct {
	Models []ModelInfo `json:"models"`
}

// ListOllamaModels retrieves all available models from the Ollama API.
func ListOllamaModels() ([]ModelInfo, error) {
	url := DefaultOllamaURL + "/api/tags"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama at %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama API request failed (status %d)", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response ListModelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model list response: %w", err)
	}

	return response.Models, nil
}
