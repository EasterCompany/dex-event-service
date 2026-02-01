package utils

import (
	sharedUtils "github.com/EasterCompany/dex-go-utils/utils"
	"time"
)

// FetchURL makes an HTTP GET request to the specified URL and returns the response body as a string.
// It now uses the shared implementation from dex-go-utils.
func FetchURL(url string, timeout time.Duration) (string, error) {
	return sharedUtils.FetchURL(url, timeout)
}
