package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerMapConfig represents the structure of server-map.json
type ServerMapConfig struct {
	Servers map[string]Server `json:"servers"`
}

// Server represents a single server in the server map
type Server struct {
	User        string   `json:"user"`
	Key         string   `json:"key"`
	PublicIPV4  string   `json:"public_ipv4"`
	PrivateIPV4 string   `json:"private_ipv4"`
	PublicIPV6  string   `json:"public_ipv6"`
	Services    []string `json:"services,omitempty"`
}

// LoadServerMap loads the server map from the standard Dexter config location.
func LoadServerMap() (*ServerMapConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "server-map.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read server-map.json at %s: %w", path, err)
	}

	var config ServerMapConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal server-map.json: %w", err)
	}

	return &config, nil
}
