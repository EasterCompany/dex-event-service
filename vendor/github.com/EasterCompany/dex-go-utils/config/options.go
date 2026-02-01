package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// OptionsConfig represents the structure of options.json
type OptionsConfig struct {
	Doc            string                            `json:"_doc"`
	Editor         string                            `json:"editor"`
	Theme          string                            `json:"theme"`
	Logging        bool                              `json:"logging"`
	AccessibleDirs []string                          `json:"accessible_dirs"`
	Discord        DiscordOptions                    `json:"discord"`
	Fabricator     FabricatorOptions                 `json:"fabricator"`
	Services       map[string]map[string]interface{} `json:"services"`
	Cognitive      CognitiveOptions                  `json:"cognitive"`
}

// FabricatorOptions holds configuration for the Dex Fabricator CLI
type FabricatorOptions struct {
	OAuthClientID     string `json:"oauth_client_id"`
	OAuthClientSecret string `json:"oauth_client_secret"`
	GCPProjectID      string `json:"gcp_project_id"`
}

// CognitiveOptions holds configuration for model placement and optimization
type CognitiveOptions struct {
}

// DiscordOptions holds Discord-specific settings
type DiscordOptions struct {
	Token               string     `json:"token"`
	ServerID            string     `json:"server_id"`
	DebugChannelID      string     `json:"debug_channel_id"`
	MasterUser          string     `json:"master_user"`
	DefaultVoiceChannel string     `json:"default_voice_channel"`
	QuietMode           bool       `json:"quiet_mode"`
	Roles               RoleConfig `json:"roles"`
}

// RoleConfig holds role ID mapping
type RoleConfig struct {
	Admin       string `json:"admin"`
	Moderator   string `json:"moderator"`
	Contributor string `json:"contributor"`
	User        string `json:"user"`
}

// LoadOptions loads the options from the standard Dexter config location
func LoadOptions() (*OptionsConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "options.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("could not read options.json at %s: %w", path, err)
	}

	var config OptionsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal options.json: %w", err)
	}

	return &config, nil
}

// SaveOptions saves the options.json file to the standard Dexter config location
func SaveOptions(options *OptionsConfig) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "options.json")
	data, err := json.MarshalIndent(options, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal options.json: %w", err)
	}

	return os.WriteFile(path, data, 0o644)
}
