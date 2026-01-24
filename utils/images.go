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

	"golang.org/x/image/draw"
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

	// Decode the image (handles GIF, JPEG, PNG, WebP)
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to decode image: %v", err)
	}

	// AGGRESSIVE PRE-SCALING
	// Qwen2-VL works best with multiples of 28. 672 is a good balance (24 patches).
	maxDim := 672
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width > maxDim || height > maxDim {
		var newW, newH int
		if width > height {
			newW = maxDim
			newH = (height * maxDim) / width
		} else {
			newH = maxDim
			newW = (width * maxDim) / height
		}

		// Ensure multiples of 28 for Qwen2-VL alignment
		newW = (newW / 28) * 28
		newH = (newH / 28) * 28
		if newW == 0 {
			newW = 28
		}
		if newH == 0 {
			newH = 28
		}

		dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		img = dst
	}

	// Encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return "", fmt.Errorf("failed to encode as jpeg: %v", err)
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
