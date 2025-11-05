package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandPath resolves paths like "~/" to the user's home directory.
func expandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get user home directory: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// getConfigPath constructs the full path to a config file in ~/Dexter/config.
func getConfigPath(filename string) (string, error) {
	return expandPath(filepath.Join("~/Dexter/config", filename))
}

// loadAndUnmarshal reads a JSON file from the config directory and unmarshals it into the provided interface.
func loadAndUnmarshal(filename string, v interface{}) error {
	path, err := getConfigPath(filename)
	if err != nil {
		return fmt.Errorf("could not get config path for %s: %w", filename, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read config file %s: %w", filename, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("could not parse config file %s: %w", filename, err)
	}

	return nil
}

// LoadServiceMap loads the service-map.json file.
func LoadServiceMap() (*ServiceMapConfig, error) {
	var cfg ServiceMapConfig
	err := loadAndUnmarshal("service-map.json", &cfg)
	return &cfg, err
}

// LoadOptions loads the options.json file.
func LoadOptions() (*OptionsConfig, error) {
	var cfg OptionsConfig
	err := loadAndUnmarshal("options.json", &cfg)
	return &cfg, err
}

// LoadSystem loads the system.json file.
func LoadSystem() (*SystemConfig, error) {
	var cfg SystemConfig
	err := loadAndUnmarshal("system.json", &cfg)
	return &cfg, err
}
