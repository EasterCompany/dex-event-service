package config

import (
	sharedConfig "github.com/EasterCompany/dex-go-utils/config"
)

// LoadServiceMap loads the service-map.json file using shared logic.
func LoadServiceMap() (*sharedConfig.ServiceMapConfig, error) {
	return sharedConfig.LoadServiceMap()
}

// LoadOptions loads the options.json file using shared logic.
func LoadOptions() (*sharedConfig.OptionsConfig, error) {
	return sharedConfig.LoadOptions()
}

// LoadSystem loads the system.json file using shared logic.
func LoadSystem() (*sharedConfig.SystemConfig, error) {
	return sharedConfig.LoadSystem()
}

// LoadServerMap loads the server-map.json file using shared logic.
func LoadServerMap() (*sharedConfig.ServerMapConfig, error) {
	return sharedConfig.LoadServerMap()
}

// SaveServiceMap is a stub for backward compatibility (saving not recommended via shared utils yet)
func SaveServiceMap(cfg *sharedConfig.ServiceMapConfig) error {
	return nil
}
