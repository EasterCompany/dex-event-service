package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL string
}

func NewClient(url string) *Client {
	return &Client{BaseURL: url}
}

type MetadataResponse struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	Content     string `json:"content,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Error       string `json:"error,omitempty"`
}

// WebViewResponse matches the structure returned by the /webview endpoint in dex-web-service
type WebViewResponse struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Content    string `json:"content,omitempty"`    // Rendered HTML content
	Screenshot string `json:"screenshot,omitempty"` // Base64 encoded screenshot
	Error      string `json:"error,omitempty"`
}

func (c *Client) FetchMetadata(linkURL string) (*MetadataResponse, error) {
	reqURL := fmt.Sprintf("%s/metadata?url=%s", c.BaseURL, url.QueryEscape(linkURL))

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

// FetchWebView calls the /webview endpoint of dex-web-service to get headless browser data.
func (c *Client) FetchWebView(linkURL string) (*WebViewResponse, error) {
	reqURL := fmt.Sprintf("%s/webview?url=%s", c.BaseURL, url.QueryEscape(linkURL))

	// Webview calls can take longer due to browser startup and page rendering
	client := &http.Client{Timeout: 45 * time.Second} // Increased timeout
	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("failed to call web view service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("web view service returned status %d: %s", resp.StatusCode, string(body))
	}

	var webViewResp WebViewResponse
	if err := json.NewDecoder(resp.Body).Decode(&webViewResp); err != nil {
		return nil, fmt.Errorf("failed to decode web view service response: %w", err)
	}

	// Propagate error from the webview response itself
	if webViewResp.Error != "" {
		return nil, fmt.Errorf("web view service reported error: %s", webViewResp.Error)
	}

	return &webViewResp, nil
}
