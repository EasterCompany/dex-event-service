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

// SaveServerMap saves the server map to the standard Dexter config location.
func SaveServerMap(config *ServerMapConfig) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "server-map.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal server map: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// GetServerForHost finds the server configuration for a given host (IP or hostname).
func (sm *ServerMapConfig) GetServerForHost(host string) (*Server, error) {
	// 1. Direct key match
	if server, ok := sm.Servers[host]; ok {
		return &server, nil
	}

	// 2. IP match
	for _, server := range sm.Servers {
		if server.PublicIPV4 == host || server.PrivateIPV4 == host || server.PublicIPV6 == host {
			return &server, nil
		}
	}

	return nil, fmt.Errorf("server config not found for host: %s", host)
}
