package config

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
