package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// OptionsConfig represents the structure of options.json
type OptionsConfig struct {
	Editor    string           `json:"editor"`
	Theme     string           `json:"theme"`
	Logging   bool             `json:"logging"`
	Discord   DiscordOptions   `json:"discord"`
	Cognitive CognitiveOptions `json:"cognitive"`
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
		return nil, fmt.Errorf("could not read options.json at %s: %w", path, err)
	}

	var config OptionsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal options.json: %w", err)
	}

	return &config, nil
}
