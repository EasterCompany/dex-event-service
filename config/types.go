package config

// GetSanitized returns a sanitized version of the config.
func (c *ServiceMapConfig) GetSanitized() map[string]interface{} {
	sanitized := make(map[string]interface{})
	sanitized["service_types"] = c.ServiceTypes
	sanitized["services"] = c.Services
	return sanitized
}

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
	ID          string      `json:"id"`
	Repo        string      `json:"repo"`
	Source      string      `json:"source"`
	Domain      string      `json:"domain,omitempty"`
	Port        string      `json:"port,omitempty"`
	Credentials interface{} `json:"credentials,omitempty"` // Using interface{} for flexibility
}

// OptionsConfig represents the structure of options.json
type OptionsConfig struct {
	Editor  string         `json:"editor"`
	Theme   string         `json:"theme"`
	Logging bool           `json:"logging"`
	Discord DiscordOptions `json:"discord"`
}

// DiscordOptions holds Discord-specific settings
type DiscordOptions struct {
	Token          string `json:"token"`
	ServerID       string `json:"server_id"`
	DebugChannelID string `json:"debug_channel_id"`
}

// SystemConfig represents the structure of system.json
type SystemConfig struct {
	MemoryBytes int64         `json:"MEMORY_BYTES"`
	CPU         []CPUInfo     `json:"CPU"`
	GPU         []GPUInfo     `json:"GPU"`
	Storage     []StorageInfo `json:"STORAGE"`
	Packages    []PackageInfo `json:"PACKAGES"`
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
	VRAM             int    `json:"VRAM"`
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
