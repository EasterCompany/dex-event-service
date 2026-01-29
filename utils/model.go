package utils

import (
	"os"
	"path/filepath"
	"time"
)

// ModelInfo reflects a single model entry.
type ModelInfo struct {
	Name       string    `json:"name"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
}

// ListHubModels retrieves all available models by scanning the Dexter models directory.
func ListHubModels() ([]ModelInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	modelDir := filepath.Join(home, "Dexter", "models")

	files, err := os.ReadDir(modelDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ModelInfo{}, nil
		}
		return nil, err
	}

	var models []ModelInfo
	for _, f := range files {
		if !f.IsDir() {
			info, err := f.Info()
			if err != nil {
				continue
			}
			models = append(models, ModelInfo{
				Name:       f.Name(),
				ModifiedAt: info.ModTime(),
				Size:       info.Size(),
			})
		}
	}

	return models, nil
}
