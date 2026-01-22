package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	Summary     string `json:"summary,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Error       string `json:"error,omitempty"`
}

// WebViewResponse matches the structure returned by the /webview endpoint in dex-web-service
type WebViewResponse struct {
	URL            string `json:"url"`
	Title          string `json:"title,omitempty"`
	Content        string `json:"content,omitempty"`         // Rendered HTML content
	Summary        string `json:"summary,omitempty"`         // Generated summary
	Screenshot     string `json:"screenshot,omitempty"`      // Base64 encoded screenshot
	ScreenshotPath string `json:"screenshot_path,omitempty"` // Local path to screenshot
	Error          string `json:"error,omitempty"`
}

func (c *Client) FetchMetadata(linkURL string, summary bool) (*MetadataResponse, error) {
	reqURL := fmt.Sprintf("%s/metadata?url=%s", c.BaseURL, url.QueryEscape(linkURL))
	if summary {
		reqURL += "&summary=true"
	}

	client := &http.Client{Timeout: 60 * time.Second} // Increased timeout for possible summary generation
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
func (c *Client) FetchWebView(linkURL string, summary bool) (*WebViewResponse, error) {
	// Create temp path for screenshot
	tmpDir := "/tmp/dexter/images"
	_ = os.MkdirAll(tmpDir, 0777)
	filename := fmt.Sprintf("webview-%d.png", time.Now().UnixNano())
	screenshotPath := filepath.Join(tmpDir, filename)

	reqURL := fmt.Sprintf("%s/webview?url=%s&output_path=%s", c.BaseURL, url.QueryEscape(linkURL), url.QueryEscape(screenshotPath))
	if summary {
		reqURL += "&summary=true"
	}

	// Webview calls can take longer due to browser startup and page rendering
	client := &http.Client{Timeout: 90 * time.Second} // Increased timeout for browser + summary
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

	// If we got a path back, read it and populate Screenshot (base64) for backward compat / usage
	if webViewResp.ScreenshotPath != "" {
		data, err := os.ReadFile(webViewResp.ScreenshotPath)
		if err == nil {
			webViewResp.Screenshot = base64.StdEncoding.EncodeToString(data)
			// Clean up file if we are consuming it immediately
			_ = os.Remove(webViewResp.ScreenshotPath)
		} else {
			// If file read fails, we might still have screenshot in payload if fallback triggered
			if webViewResp.Screenshot == "" {
				return nil, fmt.Errorf("failed to read screenshot from path: %w", err)
			}
		}
	}

	return &webViewResp, nil
}

// PerformScrape calls the /scrape endpoint of dex-web-service to get high-fidelity markdown content.
func (c *Client) PerformScrape(ctx context.Context, linkURL string) (*MetadataResponse, error) {
	reqURL := fmt.Sprintf("%s/scrape?url=%s", c.BaseURL, url.QueryEscape(linkURL))

	req, _ := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call scrape service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("scrape service returned status %d: %s", resp.StatusCode, string(body))
	}

	var metaResp MetadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&metaResp); err != nil {
		return nil, fmt.Errorf("failed to decode scrape service response: %w", err)
	}

	return &metaResp, nil
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// PerformSearch calls the /search endpoint of dex-web-service.
func (c *Client) PerformSearch(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	reqURL := fmt.Sprintf("%s/search?q=%s&limit=%d", c.BaseURL, url.QueryEscape(query), limit)

	req, _ := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call search service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search service returned status %d: %s", resp.StatusCode, string(body))
	}

	var results []SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode search service response: %w", err)
	}

	return results, nil
}
