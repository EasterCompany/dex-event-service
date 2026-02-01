package utils

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// FetchURL makes an HTTP GET request to the specified URL and returns the response body as a string.
// It applies a given timeout to the request.
func FetchURL(url string, timeout time.Duration) (string, error) {
	client := http.Client{
		Timeout: timeout,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to make HTTP request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("received non-OK HTTP status for %s: %s", url, resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from %s: %w", url, err)
	}

	return string(bodyBytes), nil
}
