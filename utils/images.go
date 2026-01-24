package utils

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif" // Register GIF decoder
	"image/jpeg"
	_ "image/png" // Register PNG decoder
	"net/http"

	_ "golang.org/x/image/webp" // Register WebP decoder
)

func DownloadImageAndConvertToJPEG(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image: status %d", resp.StatusCode)
	}

	// Decode the image (handles GIF, JPEG, PNG automatically if registered)
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to decode image: %v", err)
	}

	// Encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return "", fmt.Errorf("failed to encode as jpeg: %v", err)
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
