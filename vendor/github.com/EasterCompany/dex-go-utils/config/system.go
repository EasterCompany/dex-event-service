package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SystemConfig represents the structure of system.json

type SystemConfig struct {
	OS string `json:"OS"`

	Architecture string `json:"ARCHITECTURE"`

	MemoryBytes int64 `json:"MEMORY_BYTES"`

	CPU []CPUInfo `json:"CPU"`

	GPU []GPUInfo `json:"GPU"`

	Storage []StorageInfo `json:"STORAGE"`

	Packages []PackageInfo `json:"PACKAGES"`
}

// LoadSystem loads the system configuration from the standard Dexter config location.
func LoadSystem() (*SystemConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get home directory: %w", err)
	}

	path := filepath.Join(home, "Dexter", "config", "system.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read system.json at %s: %w", path, err)
	}

	var config SystemConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("could not unmarshal system.json: %w", err)
	}

	return &config, nil
}

// CPUInfo holds details about a CPU
type CPUInfo struct {
	Label   string  `json:"LABEL"`
	Count   int     `json:"COUNT"`
	Threads int     `json:"THREADS"`
	AvgGHz  float64 `json:"AVG_GHZ"`
	MaxGHz  float64 `json:"MAX_GHZ"`
}

// GPUInfo holds details about a GPU
type GPUInfo struct {
	Label            string `json:"LABEL"`
	CUDA             int    `json:"CUDA"`
	VRAM             int64  `json:"VRAM"`
	ComputePriority  int    `json:"COMPUTE_PRIORITY"`
	ComputePotential int    `json:"COMPUTE_POTENTIAL"`
}

// StorageInfo holds details about a storage device
type StorageInfo struct {
	Device     string `json:"DEVICE"`
	Size       int64  `json:"SIZE"`
	Used       int64  `json:"USED"`
	MountPoint string `json:"MOUNT_POINT"`
}

// PackageInfo holds details about a system package
type PackageInfo struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Required       bool   `json:"required"`
	MinVersion     string `json:"min_version,omitempty"`
	Installed      bool   `json:"installed"`
	InstallCommand string `json:"install_command"`
	UpgradeCommand string `json:"upgrade_command,omitempty"`
}
