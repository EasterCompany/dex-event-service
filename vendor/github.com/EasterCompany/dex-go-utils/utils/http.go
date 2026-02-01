package utils

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// WaitForServer waits for a server to be ready by polling its health endpoint.
func WaitForServer(port string, timeoutSeconds int) error {
	url := fmt.Sprintf("http://127.0.0.1:%s/health", port)
	for i := 0; i < timeoutSeconds; i++ {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for server on port %s", port)
}

// FetchURL makes an HTTP GET request to the specified URL and returns the response body as a string.
func FetchURL(url string, timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout: timeout,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from %s: %w", url, err)
	}

	return string(body), nil
}
