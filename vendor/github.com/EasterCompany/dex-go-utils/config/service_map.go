package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServiceMapConfig represents the structure of service-map.json
type ServiceMapConfig struct {
	ServiceTypes []ServiceType             `json:"service_types"`
	Services     map[string][]ServiceEntry `json:"services"`
}

// ServiceType defines a category of services
type ServiceType struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	MinPort     int    `json:"min_port"`
	MaxPort     int    `json:"max_port"`
}

// ServiceEntry represents a single service in the service map
type ServiceEntry struct {
	ID          string              `json:"id"`
	Repo        string              `json:"repo"`
	Source      string              `json:"source"`
	Domain      string              `json:"domain,omitempty"`
	Port        string              `json:"port,omitempty"`
	Credentials *ServiceCredentials `json:"credentials,omitempty"`
}

// ServiceCredentials holds connection credentials for services like Redis
type ServiceCredentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
	DB       int    `json:"db"`
}

// LoadServiceMap loads the service map from the standard Dexter config location
func LoadServiceMap() (*ServiceMapConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "service-map.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read service-map.json at %s: %w", path, err)
	}

	var config ServiceMapConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal service-map.json: %w", err)
	}

	return &config, nil
}

// GetServiceURL finds a service by ID and category and returns its full HTTP URL
func (s *ServiceMapConfig) GetServiceURL(id, category, defaultPort string) string {
	for _, entry := range s.Services[category] {
		if entry.ID == id {
			host := entry.Domain
			if host == "" {
				host = "127.0.0.1"
			}
			return fmt.Sprintf("http://%s:%s", host, entry.Port)
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%s", defaultPort)
}

// ResolveHubURL specifically finds the dex-model-service URL
func (s *ServiceMapConfig) ResolveHubURL() string {
	return s.GetServiceURL("dex-model-service", "co", "8400")
}

// GetSanitized returns a version of the map with credentials masked (stub for now if needed)
func (c *ServiceMapConfig) GetSanitized() map[string]interface{} {
	sanitized := make(map[string]interface{})
	sanitized["service_types"] = c.ServiceTypes
	sanitized["services"] = c.Services
	return sanitized
}
